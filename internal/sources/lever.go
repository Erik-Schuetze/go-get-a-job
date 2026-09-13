package sources

import (
	"context"
	"fmt"
	"net/http"

	"github.com/Erik-Schuetze/go-get-a-job/internal/httpbody"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

const leverDefaultBaseURL = "https://api.lever.co/v0/postings"

// Lever fetches postings from a company's public Lever job-board API:
// GET https://api.lever.co/v0/postings/{company}?mode=json
//
// This is a free, unauthenticated, public API (verified live during
// planning against Spotify's board).
type Lever struct {
	Company     string
	DisplayName string

	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

// NewLever builds a Lever source for the given company slug and
// human-readable display name.
func NewLever(company, displayName string) *Lever {
	return &Lever{
		Company:          company,
		DisplayName:      displayName,
		BaseURL:          leverDefaultBaseURL,
		HTTPClient:       defaultHTTPClient(),
		MaxResponseBytes: httpbody.MaxAPIBytes,
	}
}

func (l *Lever) Name() string { return "lever" }

func (l *Lever) Label() string { return "lever/" + l.Company }

type leverPosting struct {
	ID               string          `json:"id"`
	Text             string          `json:"text"`
	HostedURL        string          `json:"hostedUrl"`
	DescriptionPlain string          `json:"descriptionPlain"`
	CreatedAt        int64           `json:"createdAt"`
	Categories       leverCategories `json:"categories"`

	// WorkplaceType is the posting-level answer to "where does this happen"
	// ("remote", "hybrid", "on-site"); it was already on the wire and
	// unused. Display only.
	WorkplaceType string `json:"workplaceType"`
}

// leverCategories carries the classification Lever groups a posting under.
// EmploymentType is taken from Commitment, which is Lever's own name for the
// engagement ("Full-time", "Contract"); both are display-only.
type leverCategories struct {
	Location   string `json:"location"`
	Team       string `json:"team"`
	Department string `json:"department"`
	Commitment string `json:"commitment"`
}

// Fetch retrieves every open posting on this company's Lever board.
func (l *Lever) Fetch(ctx context.Context) ([]model.Job, error) {
	url, err := buildURL(l.BaseURL, "mode=json", l.Company)
	if err != nil {
		return nil, fmt.Errorf("lever(%s): %w", l.Company, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("lever(%s): building request: %w", l.Company, err)
	}

	resp, err := l.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("lever(%s): request failed: %w", l.Company, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("lever(%s): unexpected status %d", l.Company, resp.StatusCode)
	}

	var postings []leverPosting
	if err := httpbody.DecodeJSON(resp.Body, l.MaxResponseBytes, &postings); err != nil {
		return nil, fmt.Errorf("lever(%s): reading response: %w", l.Company, err)
	}

	jobs := make([]model.Job, 0, len(postings))
	for _, p := range postings {
		jobs = append(jobs, model.Job{
			ID:          fmt.Sprintf("lever:%s:%s", l.Company, p.ID),
			Source:      l.Name(),
			Company:     l.DisplayName,
			Title:       p.Text,
			Location:    p.Categories.Location,
			URL:         p.HostedURL,
			Description: p.DescriptionPlain,
			PostedAt:    msToTime(p.CreatedAt),

			Department:     joinNonEmpty(" · ", p.Categories.Department, p.Categories.Team),
			WorkplaceType:  p.WorkplaceType,
			EmploymentType: p.Categories.Commitment,
		})
	}
	return jobs, nil
}
