package sources

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/Erik-Schuetze/go-get-a-job/internal/httpbody"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

const greenhouseDefaultBaseURL = "https://boards-api.greenhouse.io/v1/boards"

// Greenhouse fetches postings from a company's public Greenhouse job-board
// API: GET https://boards-api.greenhouse.io/v1/boards/{company}/jobs
//
// This is a free, unauthenticated, public API used by many tech companies
// (confirmed live during planning for Grafana Labs, GitLab, and HashiCorp).
type Greenhouse struct {
	Company     string
	DisplayName string

	// BaseURL, HTTPClient, and MaxResponseBytes are overridable (e.g. for
	// tests against a fake server); NewGreenhouse sets sane defaults for
	// real use.
	BaseURL    string
	HTTPClient *http.Client
	// MaxResponseBytes caps how much of the response is read; see
	// internal/httpbody. A large board with descriptions inline is the
	// biggest legitimate response in the pipeline.
	MaxResponseBytes int64
}

// NewGreenhouse builds a Greenhouse source for the given board token
// (company) and human-readable display name.
func NewGreenhouse(company, displayName string) *Greenhouse {
	return &Greenhouse{
		Company:          company,
		DisplayName:      displayName,
		BaseURL:          greenhouseDefaultBaseURL,
		HTTPClient:       defaultHTTPClient(),
		MaxResponseBytes: httpbody.MaxAPIBytes,
	}
}

func (g *Greenhouse) Name() string { return "greenhouse" }

func (g *Greenhouse) Label() string { return "greenhouse/" + g.Company }

type greenhouseResponse struct {
	Jobs []greenhouseJob `json:"jobs"`
}

type greenhouseJob struct {
	ID             int64              `json:"id"`
	Title          string             `json:"title"`
	AbsoluteURL    string             `json:"absolute_url"`
	Content        string             `json:"content"`
	Location       greenhouseLocation `json:"location"`
	FirstPublished string             `json:"first_published"`
	Departments    []greenhouseDept   `json:"departments"`
}

type greenhouseLocation struct {
	Name string `json:"name"`
}

// greenhouseDept is one entry of a posting's departments array. Greenhouse
// nests a child array inside each department for sub-departments, but the
// parent name is the one that says what the team is ("R&D: Platform"), so the
// children are not decoded.
type greenhouseDept struct {
	Name string `json:"name"`
}

// Fetch retrieves every open posting on this company's Greenhouse board.
func (g *Greenhouse) Fetch(ctx context.Context) ([]model.Job, error) {
	url, err := buildURL(g.BaseURL, "content=true", g.Company, "jobs")
	if err != nil {
		return nil, fmt.Errorf("greenhouse(%s): %w", g.Company, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("greenhouse(%s): building request: %w", g.Company, err)
	}

	resp, err := g.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("greenhouse(%s): request failed: %w", g.Company, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("greenhouse(%s): unexpected status %d", g.Company, resp.StatusCode)
	}

	var parsed greenhouseResponse
	if err := httpbody.DecodeJSON(resp.Body, g.MaxResponseBytes, &parsed); err != nil {
		return nil, fmt.Errorf("greenhouse(%s): reading response: %w", g.Company, err)
	}

	jobs := make([]model.Job, 0, len(parsed.Jobs))
	for _, j := range parsed.Jobs {
		jobs = append(jobs, model.Job{
			ID:          fmt.Sprintf("greenhouse:%s:%d", g.Company, j.ID),
			Source:      g.Name(),
			Company:     g.DisplayName,
			Title:       j.Title,
			Location:    j.Location.Name,
			URL:         j.AbsoluteURL,
			Description: stripHTML(j.Content),
			PostedAt:    parseRFC3339Best(j.FirstPublished),
			Department:  greenhouseDepartment(j.Departments),
		})
	}
	return jobs, nil
}

// greenhouseDepartment joins the posting's department names into one label.
// A posting can belong to several, and dropping all but the first loses real
// information; the same join-and-sanitize path every other display-only field
// goes through keeps the length bounded.
func greenhouseDepartment(depts []greenhouseDept) string {
	names := make([]string, 0, len(depts))
	for _, d := range depts {
		if name := strings.TrimSpace(d.Name); name != "" {
			names = append(names, name)
		}
	}
	return strings.Join(names, ", ")
}
