package sources

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Erik-Schuetze/go-get-a-job/internal/httpbody"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

const ashbyDefaultBaseURL = "https://api.ashbyhq.com/posting-api/job-board"

// Ashby fetches postings from a company's public Ashby job-board API:
// GET https://api.ashbyhq.com/posting-api/job-board/{company}
//
// This is a free, unauthenticated, public API (verified live during
// planning against Notion's board). Note it's a GET request despite some
// third-party documentation suggesting POST.
type Ashby struct {
	Company     string
	DisplayName string

	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

// NewAshby builds an Ashby source for the given company slug and
// human-readable display name.
func NewAshby(company, displayName string) *Ashby {
	return &Ashby{
		Company:          company,
		DisplayName:      displayName,
		BaseURL:          ashbyDefaultBaseURL,
		HTTPClient:       defaultHTTPClient(),
		MaxResponseBytes: httpbody.MaxAPIBytes,
	}
}

func (a *Ashby) Name() string { return "ashby" }

func (a *Ashby) Label() string { return "ashby/" + a.Company }

type ashbyResponse struct {
	Jobs []ashbyJob `json:"jobs"`
}

type ashbyJob struct {
	ID               string `json:"id"`
	Title            string `json:"title"`
	Location         string `json:"location"`
	JobURL           string `json:"jobUrl"`
	IsListed         bool   `json:"isListed"`
	PublishedAt      string `json:"publishedAt"`
	DescriptionPlain string `json:"descriptionPlain"`
}

// Fetch retrieves every open, listed posting on this company's Ashby
// board.
func (a *Ashby) Fetch(ctx context.Context) ([]model.Job, error) {
	url, err := buildURL(a.BaseURL, "", a.Company)
	if err != nil {
		return nil, fmt.Errorf("ashby(%s): %w", a.Company, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("ashby(%s): building request: %w", a.Company, err)
	}

	resp, err := a.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ashby(%s): request failed: %w", a.Company, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ashby(%s): unexpected status %d", a.Company, resp.StatusCode)
	}

	var parsed ashbyResponse
	if err := httpbody.DecodeJSON(resp.Body, a.MaxResponseBytes, &parsed); err != nil {
		return nil, fmt.Errorf("ashby(%s): reading response: %w", a.Company, err)
	}

	jobs := make([]model.Job, 0, len(parsed.Jobs))
	for _, j := range parsed.Jobs {
		if !j.IsListed {
			continue
		}
		jobs = append(jobs, model.Job{
			ID:          fmt.Sprintf("ashby:%s:%s", a.Company, j.ID),
			Source:      a.Name(),
			Company:     a.DisplayName,
			Title:       j.Title,
			Location:    j.Location,
			URL:         j.JobURL,
			Description: j.DescriptionPlain,
			PostedAt:    parseRFC3339Best(j.PublishedAt),
		})
	}
	return jobs, nil
}
