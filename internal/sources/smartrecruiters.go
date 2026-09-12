package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

const (
	smartRecruitersDefaultBaseURL = "https://api.smartrecruiters.com/v1/companies"
	smartRecruitersPageSize       = 100
	// smartRecruitersMaxResults is a safety cap against runaway pagination
	// on unexpectedly huge boards; SmartRecruiters customers are typically
	// well under this.
	smartRecruitersMaxResults = 2000
	// smartRecruitersDetailWorkers bounds how many concurrent per-posting
	// detail requests we make (the list endpoint doesn't include the
	// public URL or description, so a second request per posting is
	// required - see fetchDetail).
	smartRecruitersDetailWorkers = 8
)

// SmartRecruiters fetches postings from a company's public SmartRecruiters
// job-board API. The list endpoint
// (GET https://api.smartrecruiters.com/v1/companies/{company}/postings)
// only returns title/location/id - the public posting URL and description
// are only available from the per-posting detail endpoint
// (GET .../postings/{id}), which this connector fetches for every listed
// posting using a small bounded worker pool. Both endpoints are free and
// unauthenticated (verified live during planning).
type SmartRecruiters struct {
	Company     string
	DisplayName string

	BaseURL    string
	HTTPClient *http.Client
}

// NewSmartRecruiters builds a SmartRecruiters source for the given company
// identifier and human-readable display name.
func NewSmartRecruiters(company, displayName string) *SmartRecruiters {
	return &SmartRecruiters{
		Company:     company,
		DisplayName: displayName,
		BaseURL:     smartRecruitersDefaultBaseURL,
		HTTPClient:  defaultHTTPClient(),
	}
}

func (s *SmartRecruiters) Name() string { return "smartrecruiters" }

type smartRecruitersListResponse struct {
	TotalFound int                       `json:"totalFound"`
	Content    []smartRecruitersListItem `json:"content"`
}

type smartRecruitersListItem struct {
	ID           string                  `json:"id"`
	Name         string                  `json:"name"`
	ReleasedDate string                  `json:"releasedDate"`
	Location     smartRecruitersLocation `json:"location"`
}

type smartRecruitersLocation struct {
	FullLocation string `json:"fullLocation"`
}

type smartRecruitersDetail struct {
	PostingURL string               `json:"postingUrl"`
	JobAd      smartRecruitersJobAd `json:"jobAd"`
}

type smartRecruitersJobAd struct {
	Sections smartRecruitersSections `json:"sections"`
}

type smartRecruitersSections struct {
	JobDescription *smartRecruitersSection `json:"jobDescription"`
	Qualifications *smartRecruitersSection `json:"qualifications"`
}

type smartRecruitersSection struct {
	Text string `json:"text"`
}

// Fetch retrieves every open posting on this company's SmartRecruiters
// board, enriched with the description and public URL from each
// posting's detail endpoint. Individual detail-fetch failures are
// skipped (not fatal) so one bad posting can't sink an entire run.
func (s *SmartRecruiters) Fetch(ctx context.Context) ([]model.Job, error) {
	items, err := s.fetchList(ctx)
	if err != nil {
		return nil, err
	}

	results := make([]*model.Job, len(items))
	forEachBounded(ctx, len(items), smartRecruitersDetailWorkers, func(i int) {
		job, err := s.fetchDetail(ctx, items[i])
		if err != nil {
			return
		}
		results[i] = job
	})

	jobs := make([]model.Job, 0, len(items))
	for _, j := range results {
		if j != nil {
			jobs = append(jobs, *j)
		}
	}
	return jobs, nil
}

func (s *SmartRecruiters) fetchList(ctx context.Context) ([]smartRecruitersListItem, error) {
	var all []smartRecruitersListItem
	offset := 0

	for {
		url := fmt.Sprintf("%s/%s/postings?limit=%d&offset=%d", s.BaseURL, s.Company, smartRecruitersPageSize, offset)

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("smartrecruiters(%s): building list request: %w", s.Company, err)
		}

		resp, err := s.HTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("smartrecruiters(%s): list request failed: %w", s.Company, err)
		}

		var parsed smartRecruitersListResponse
		decErr := json.NewDecoder(resp.Body).Decode(&parsed)
		status := resp.StatusCode
		resp.Body.Close()

		if status != http.StatusOK {
			return nil, fmt.Errorf("smartrecruiters(%s): unexpected status %d", s.Company, status)
		}
		if decErr != nil {
			return nil, fmt.Errorf("smartrecruiters(%s): decoding list response: %w", s.Company, decErr)
		}

		all = append(all, parsed.Content...)
		offset += len(parsed.Content)

		if len(parsed.Content) == 0 || offset >= parsed.TotalFound || offset >= smartRecruitersMaxResults {
			break
		}
	}

	return all, nil
}

func (s *SmartRecruiters) fetchDetail(ctx context.Context, item smartRecruitersListItem) (*model.Job, error) {
	url := fmt.Sprintf("%s/%s/postings/%s", s.BaseURL, s.Company, item.ID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("smartrecruiters(%s): building detail request for %s: %w", s.Company, item.ID, err)
	}

	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("smartrecruiters(%s): detail request failed for %s: %w", s.Company, item.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("smartrecruiters(%s): unexpected detail status %d for %s", s.Company, resp.StatusCode, item.ID)
	}

	var detail smartRecruitersDetail
	if err := json.NewDecoder(resp.Body).Decode(&detail); err != nil {
		return nil, fmt.Errorf("smartrecruiters(%s): decoding detail for %s: %w", s.Company, item.ID, err)
	}

	var descParts []string
	if detail.JobAd.Sections.JobDescription != nil {
		descParts = append(descParts, stripHTML(detail.JobAd.Sections.JobDescription.Text))
	}
	if detail.JobAd.Sections.Qualifications != nil {
		descParts = append(descParts, stripHTML(detail.JobAd.Sections.Qualifications.Text))
	}

	return &model.Job{
		ID:          fmt.Sprintf("smartrecruiters:%s:%s", s.Company, item.ID),
		Source:      s.Name(),
		Company:     s.DisplayName,
		Title:       item.Name,
		Location:    item.Location.FullLocation,
		URL:         detail.PostingURL,
		Description: strings.Join(descParts, "\n\n"),
		PostedAt:    parseRFC3339Best(item.ReleasedDate),
	}, nil
}
