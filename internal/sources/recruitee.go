package sources

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/httpbody"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

const recruiteeDefaultBaseURL = "https://%s.recruitee.com"

// Recruitee fetches postings from a company's public Recruitee offers API:
// GET https://{company}.recruitee.com/api/offers/
//
// Free and unauthenticated (verified live during planning against xneelo and
// Grid). An unknown company answers 404, so unlike SmartRecruiters a typo
// here is loud rather than silent.
type Recruitee struct {
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

// NewRecruitee builds a Recruitee source for the given company subdomain and
// human-readable display name.
func NewRecruitee(company, displayName string) *Recruitee {
	return &Recruitee{
		Company:          company,
		DisplayName:      displayName,
		BaseURL:          fmt.Sprintf(recruiteeDefaultBaseURL, company),
		HTTPClient:       defaultHTTPClient(),
		MaxResponseBytes: httpbody.MaxAPIBytes,
	}
}

func (r *Recruitee) Name() string { return "recruitee" }

func (r *Recruitee) Label() string { return "recruitee/" + r.Company }

type recruiteeResponse struct {
	Offers []recruiteeOffer `json:"offers"`
}

type recruiteeOffer struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	City        string `json:"city"`
	Country     string `json:"country"`
	Location    string `json:"location"`
	Department  string `json:"department"`
	CareersURL  string `json:"careers_url"`
	Description string `json:"description"`
	Remote      bool   `json:"remote"`
	CreatedAt   string `json:"created_at"`
}

// Fetch retrieves every open posting on this company's Recruitee board.
func (r *Recruitee) Fetch(ctx context.Context) ([]model.Job, error) {
	base := r.BaseURL
	url, err := buildURL(base, "", "api", "offers")
	if err != nil {
		return nil, fmt.Errorf("recruitee(%s): %w", r.Company, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("recruitee(%s): building request: %w", r.Company, err)
	}

	resp, err := r.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("recruitee(%s): request failed: %w", r.Company, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("recruitee(%s): unexpected status %d", r.Company, resp.StatusCode)
	}

	var decoded recruiteeResponse
	if err := httpbody.DecodeJSON(resp.Body, r.MaxResponseBytes, &decoded); err != nil {
		return nil, fmt.Errorf("recruitee(%s): reading response: %w", r.Company, err)
	}

	jobs := make([]model.Job, 0, len(decoded.Offers))
	for _, o := range decoded.Offers {
		if o.ID == 0 || o.Title == "" {
			continue
		}
		jobs = append(jobs, model.Job{
			ID:          fmt.Sprintf("recruitee:%s:%d", r.Company, o.ID),
			Source:      r.Name(),
			Company:     r.DisplayName,
			Title:       o.Title,
			Location:    recruiteeLocation(o),
			URL:         o.CareersURL,
			Description: stripHTML(o.Description),
			PostedAt:    recruiteeTime(o.CreatedAt),
		})
	}
	return jobs, nil
}

// recruiteeLocation joins the several location fields Recruitee spreads a
// posting across. Nothing is dropped, because the pre-filter's whole-word
// match only needs one of them to name either a workable place or a remote
// marker - and the AI scorer, not this, decides what the combination means.
// recruiteeLocation renders every place a posting carries, as comma-separated
// segments with duplicates removed. Recruitee spreads location over three
// overlapping fields - "location" is usually "City, Country" while "city" and
// "country" repeat its halves - so rendering them joined naively produces
// "Leipzig, Germany, Leipzig, Germany" and any allow-list entry matches twice.
//
// Over-inclusion is deliberate for the field that carries the remote flag:
// Recruitee files a remote posting under its office city, so the marker has to
// be present for the location pre-filter to hand the posting to the scorer
// rather than dropping it as a foreign onsite role.
func recruiteeLocation(o recruiteeOffer) string {
	segments := make([]string, 0, 4)
	add := func(value string) {
		for _, seg := range strings.Split(value, ",") {
			seg = strings.TrimSpace(seg)
			if seg == "" || containsFold(segments, seg) {
				continue
			}
			segments = append(segments, seg)
		}
	}
	if o.Remote {
		add("Remote")
	}
	for _, v := range []string{o.Location, o.City, o.Country} {
		add(v)
	}
	return strings.Join(segments, ", ")
}

// containsFold reports whether an already-collected part means the same thing
// as candidate, so "Remote, Remote job, Remote, Germany" cannot happen. Only
// the first word is compared: some boards repeat the same city under both a
// "Location" and a "City" field, but a board that says "Berlin, Germany" and
// "Germany" is naming two different things and both are kept.
func containsFold(parts []string, candidate string) bool {
	first, _, _ := strings.Cut(candidate, ",")
	for _, p := range parts {
		pFirst, _, _ := strings.Cut(p, ",")
		if strings.EqualFold(strings.TrimSpace(pFirst), strings.TrimSpace(first)) {
			return true
		}
	}
	return false
}

// recruiteeTime parses Recruitee's "2026-09-02 06:09:35 UTC" timestamp,
// returning the zero time rather than an error - PostedAt is best-effort.
func recruiteeTime(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04:05 MST", strings.TrimSpace(s))
	if err != nil {
		return time.Time{}
	}
	return t
}
