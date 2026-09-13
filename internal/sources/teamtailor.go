package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/httpbody"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

const teamtailorDefaultBaseURL = "https://%s.teamtailor.com"

// Teamtailor fetches postings from a company's public Teamtailor job feed:
// GET https://{company}.teamtailor.com/jobs.json
//
// This is a JSON Feed (https://jsonfeed.org) whose items embed a schema.org
// JobPosting under "_jobposting" - the same format the company's own careers
// page uses to get indexed by job search engines, so it is intended to be
// machine-read. Verified live during planning against Spacelift, which also
// serves the same document from its custom careers subdomain.
type Teamtailor struct {
	Company     string
	DisplayName string

	// BaseURL is where the company's feed lives. It is a whole URL rather
	// than a hostname because the company name is part of the host here (the
	// board is a subdomain), so there is no path segment to swap in. Tests
	// point it at an httptest server.
	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

// NewTeamtailor builds a Teamtailor source for the given company subdomain
// and human-readable display name.
func NewTeamtailor(company, displayName string) *Teamtailor {
	return &Teamtailor{
		Company:          company,
		DisplayName:      displayName,
		BaseURL:          fmt.Sprintf(teamtailorDefaultBaseURL, company),
		HTTPClient:       defaultHTTPClient(),
		MaxResponseBytes: httpbody.MaxAPIBytes,
	}
}

func (t *Teamtailor) Name() string { return "teamtailor" }

func (t *Teamtailor) Label() string { return "teamtailor/" + t.Company }

type teamtailorFeed struct {
	Items []teamtailorItem `json:"items"`
}

type teamtailorItem struct {
	ID            string            `json:"id"`
	Title         string            `json:"title"`
	URL           string            `json:"url"`
	DatePublished string            `json:"date_published"`
	ContentHTML   string            `json:"content_html"`
	JobPosting    teamtailorPosting `json:"_jobposting"`
}

type teamtailorPosting struct {
	Title       string              `json:"title"`
	DatePosted  string              `json:"datePosted"`
	Description string              `json:"description"`
	JobLocation teamtailorLocations `json:"jobLocation"`

	// EmploymentType and JobLocationType are schema.org JobPosting fields
	// that Teamtailor populates. Both are display only.
	EmploymentType  teamtailorStrings `json:"employmentType"`
	JobLocationType string            `json:"jobLocationType"`
}

// teamtailorStrings decodes a schema.org property that is legitimately either
// one string or an array of them. Same reasoning as teamtailorLocations: the
// single-value form is common enough that decoding only the array form would
// silently blank the field on half the boards.
type teamtailorStrings []string

func (s *teamtailorStrings) UnmarshalJSON(data []byte) error {
	var many []string
	if err := json.Unmarshal(data, &many); err == nil {
		*s = many
		return nil
	}

	var one string
	if err := json.Unmarshal(data, &one); err != nil {
		return err
	}
	*s = teamtailorStrings{one}
	return nil
}

// teamtailorWorkplaceType maps schema.org's jobLocationType to the wording the
// other connectors use. TELECOMMUTE is the only value the vocabulary defines
// for fully remote work; anything else is left blank rather than guessed at.
func teamtailorWorkplaceType(jobLocationType string) string {
	if strings.EqualFold(strings.TrimSpace(jobLocationType), "TELECOMMUTE") {
		return "Remote"
	}
	return ""
}

// teamtailorLocations exists because schema.org's jobLocation is legitimately
// either one Place or an array of them, and the two boards checked disagree.
// A plain []struct field would fail the whole source on the single-object
// form, which would be a silent loss of an entire board rather than a
// degraded one.
type teamtailorLocations []teamtailorPlace

type teamtailorPlace struct {
	Address struct {
		AddressLocality string `json:"addressLocality"`
		AddressRegion   string `json:"addressRegion"`
		AddressCountry  string `json:"addressCountry"`
	} `json:"address"`
}

func (l *teamtailorLocations) UnmarshalJSON(data []byte) error {
	var many []teamtailorPlace
	if err := json.Unmarshal(data, &many); err == nil {
		*l = many
		return nil
	}

	var one teamtailorPlace
	if err := json.Unmarshal(data, &one); err != nil {
		return err
	}
	*l = teamtailorLocations{one}
	return nil
}

// Fetch retrieves every open posting on this company's Teamtailor board.
func (t *Teamtailor) Fetch(ctx context.Context) ([]model.Job, error) {
	base := t.BaseURL
	url, err := buildURL(base, "", "jobs.json")
	if err != nil {
		return nil, fmt.Errorf("teamtailor(%s): %w", t.Company, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("teamtailor(%s): building request: %w", t.Company, err)
	}

	resp, err := t.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("teamtailor(%s): request failed: %w", t.Company, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("teamtailor(%s): unexpected status %d", t.Company, resp.StatusCode)
	}

	var feed teamtailorFeed
	if err := httpbody.DecodeJSON(resp.Body, t.MaxResponseBytes, &feed); err != nil {
		return nil, fmt.Errorf("teamtailor(%s): reading response: %w", t.Company, err)
	}

	jobs := make([]model.Job, 0, len(feed.Items))
	for _, item := range feed.Items {
		if item.ID == "" || item.Title == "" {
			continue
		}
		description := stripHTML(item.JobPosting.Description)
		if description == "" {
			description = stripHTML(item.ContentHTML)
		}
		jobs = append(jobs, model.Job{
			ID:          fmt.Sprintf("teamtailor:%s:%s", t.Company, item.ID),
			Source:      t.Name(),
			Company:     t.DisplayName,
			Title:       item.Title,
			Location:    teamtailorLocation(item.Title, item.JobPosting.JobLocation),
			URL:         item.URL,
			Description: description,
			PostedAt:    teamtailorTime(item.JobPosting.DatePosted, item.DatePublished),

			WorkplaceType:  teamtailorWorkplaceType(item.JobPosting.JobLocationType),
			EmploymentType: strings.Join(item.JobPosting.EmploymentType, ", "),
		})
	}
	return jobs, nil
}

// teamtailorLocation renders every location signal a posting carries, most
// authoritative first: the remote scope named in the title, then the
// schema.org places.
//
// The title is a location source here because Teamtailor boards routinely file
// a "Remote, European Union" role under the company's registered office and
// emit no other signal - neither jobLocationType nor
// applicantLocationRequirements was present on any Spacelift posting. Read
// alone, jobLocation would label that role "Warsaw, PL" and a Germany-scoped
// allow-list would reject precisely the postings worth finding.
func teamtailorLocation(title string, locations teamtailorLocations) string {
	parts := make([]string, 0, len(locations)+1)
	if scope := teamtailorTitleScope(title); scope != "" {
		parts = append(parts, scope)
	}
	for _, l := range locations {
		var sub []string
		for _, v := range []string{l.Address.AddressLocality, l.Address.AddressRegion, l.Address.AddressCountry} {
			if v = strings.TrimSpace(v); v != "" {
				sub = append(sub, v)
			}
		}
		if len(sub) == 0 {
			continue
		}
		joined := strings.Join(sub, ", ")
		if containsFold(parts, joined) {
			continue
		}
		parts = append(parts, joined)
	}
	return strings.Join(parts, " / ")
}

// teamtailorTitleScope pulls the trailing parenthetical a Teamtailor posting
// uses to declare where it can be done, e.g. "(Remote, European Union)",
// "(Remote Poland)", or "(Remote West Coast)". An onsite role with a city in
// parentheses is deliberately not treated as a scope: only groups that
// mention remote are, because a bare city there duplicates jobLocation.
func teamtailorTitleScope(title string) string {
	title = strings.TrimSpace(title)
	if !strings.HasSuffix(title, ")") {
		return ""
	}
	open := strings.LastIndex(title, "(")
	if open < 0 {
		return ""
	}
	inner := strings.TrimSpace(title[open+1 : len(title)-1])
	if inner == "" || !strings.Contains(strings.ToLower(inner), "remote") {
		return ""
	}
	return inner
}

// teamtailorTime prefers the schema.org datePosted and falls back to the
// feed's date_published, since the two are present on different boards.
func teamtailorTime(datePosted, datePublished string) time.Time {
	if t := parseRFC3339Best(datePosted); !t.IsZero() {
		return t
	}
	return parseRFC3339Best(datePublished)
}
