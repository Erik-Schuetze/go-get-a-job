package sources

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"strings"

	"github.com/Erik-Schuetze/go-get-a-job/internal/httpbody"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

const personioDefaultBaseURL = "https://%s.jobs.personio.de"

// Personio fetches postings from a company's public Personio XML feed:
// GET https://{company}.jobs.personio.de/xml
//
// This is a free, unauthenticated feed that Personio advertises for exactly
// this purpose (syndicating a careers page), verified live during planning
// against Stackable and Contabo. It is the one connector here that is
// XML rather than JSON, so it does its own decoding rather than using
// httpbody.DecodeJSON.
//
// Personio's raw feed carries no posting URL, only an ID. For every board
// checked, https://{company}.jobs.personio.de/job/{id} resolves to the real
// posting, which is what this builds.
type Personio struct {
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

// NewPersonio builds a Personio source for the given company subdomain and
// human-readable display name.
func NewPersonio(company, displayName string) *Personio {
	return &Personio{
		Company:          company,
		DisplayName:      displayName,
		BaseURL:          fmt.Sprintf(personioDefaultBaseURL, company),
		HTTPClient:       defaultHTTPClient(),
		MaxResponseBytes: httpbody.MaxAPIBytes,
	}
}

func (p *Personio) Name() string { return "personio" }

func (p *Personio) Label() string { return "personio/" + p.Company }

type personioFeed struct {
	Positions []personioPosition `xml:"position"`
}

type personioPosition struct {
	ID          string `xml:"id"`
	Subcompany  string `xml:"subcompany"`
	Office      string `xml:"office"`
	Department  string `xml:"department"`
	Name        string `xml:"name"`
	Schedule    string `xml:"schedule"`
	Seniority   string `xml:"seniority"`
	Keywords    string `xml:"keywords"`
	CreatedAt   string `xml:"createdAt"`
	Description struct {
		Items []struct {
			Name  string `xml:"name"`
			Value string `xml:"value"`
		} `xml:"jobDescription"`
	} `xml:"jobDescriptions"`
}

// Fetch retrieves every open posting on this company's Personio feed.
func (p *Personio) Fetch(ctx context.Context) ([]model.Job, error) {
	base := p.BaseURL
	url, err := buildURL(base, "", "xml")
	if err != nil {
		return nil, fmt.Errorf("personio(%s): %w", p.Company, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("personio(%s): building request: %w", p.Company, err)
	}

	resp, err := p.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("personio(%s): request failed: %w", p.Company, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("personio(%s): unexpected status %d", p.Company, resp.StatusCode)
	}

	body, err := httpbody.ReadAll(resp.Body, p.MaxResponseBytes)
	if err != nil {
		return nil, fmt.Errorf("personio(%s): reading response: %w", p.Company, err)
	}

	// A company that does not exist on Personio does not 404: it 307s to the
	// marketing site, which the HTTP client follows, and the result is an
	// HTML page. Decoding that as XML fails, but with a parser error that
	// reads like a schema change rather than "this board does not exist", so
	// the two cases are told apart before decoding.
	if !looksLikePersonioFeed(body) {
		return nil, fmt.Errorf("personio(%s): response is not a Personio feed (wrong company subdomain, or the board moved)", p.Company)
	}

	var feed personioFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("personio(%s): parsing feed: %w", p.Company, err)
	}

	jobs := make([]model.Job, 0, len(feed.Positions))
	for _, pos := range feed.Positions {
		if pos.ID == "" || pos.Name == "" {
			continue
		}
		// Personio's feed carries no posting URL, only the ID; this is the
		// path its own careers pages use for a single posting.
		jobURL, err := buildURL(base, "", "job", pos.ID)
		if err != nil {
			return nil, fmt.Errorf("personio(%s): building posting URL: %w", p.Company, err)
		}
		jobs = append(jobs, model.Job{
			ID:          fmt.Sprintf("personio:%s:%s", p.Company, pos.ID),
			Source:      p.Name(),
			Company:     p.DisplayName,
			Title:       pos.Name,
			Location:    pos.Office,
			URL:         jobURL,
			Description: personioDescription(pos),
			PostedAt:    parseRFC3339Best(pos.CreatedAt),
		})
	}
	return jobs, nil
}

// looksLikePersonioFeed reports whether the body is the feed this connector
// expects, without committing to a full parse.
func looksLikePersonioFeed(body []byte) bool {
	head := body
	if len(head) > 512 {
		head = head[:512]
	}
	return strings.Contains(string(head), "<workzag-jobs")
}

// personioDescription joins the feed's per-section descriptions into one
// block. Personio splits a posting into named sections ("Deine Aufgaben",
// "Dein Profil"), and keeping the headings is what makes the text readable
// for keyword matching and for the AI scorer.
func personioDescription(pos personioPosition) string {
	var b strings.Builder
	for _, item := range pos.Description.Items {
		text := stripHTML(item.Value)
		if text == "" {
			continue
		}
		if name := strings.TrimSpace(stripHTML(item.Name)); name != "" {
			b.WriteString(name)
			b.WriteString("\n")
		}
		b.WriteString(text)
		b.WriteString("\n\n")
	}
	if b.Len() == 0 {
		return strings.TrimSpace(pos.Keywords)
	}
	return strings.TrimSpace(b.String())
}
