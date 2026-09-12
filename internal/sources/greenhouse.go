package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

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

	// BaseURL and HTTPClient are overridable (e.g. for tests against a fake
	// server); NewGreenhouse sets sane defaults for real use.
	BaseURL    string
	HTTPClient *http.Client
}

// NewGreenhouse builds a Greenhouse source for the given board token
// (company) and human-readable display name.
func NewGreenhouse(company, displayName string) *Greenhouse {
	return &Greenhouse{
		Company:     company,
		DisplayName: displayName,
		BaseURL:     greenhouseDefaultBaseURL,
		HTTPClient:  defaultHTTPClient(),
	}
}

func (g *Greenhouse) Name() string { return "greenhouse" }

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
}

type greenhouseLocation struct {
	Name string `json:"name"`
}

// Fetch retrieves every open posting on this company's Greenhouse board.
func (g *Greenhouse) Fetch(ctx context.Context) ([]model.Job, error) {
	url := fmt.Sprintf("%s/%s/jobs?content=true", g.BaseURL, g.Company)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("greenhouse(%s): building request: %w", g.Company, err)
	}

	resp, err := g.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("greenhouse(%s): request failed: %w", g.Company, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("greenhouse(%s): unexpected status %d", g.Company, resp.StatusCode)
	}

	var parsed greenhouseResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("greenhouse(%s): decoding response: %w", g.Company, err)
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
		})
	}
	return jobs, nil
}
