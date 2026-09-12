package sources

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/Erik-Schuetze/go-get-a-job/internal/httpbody"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

const (
	// workdayPageSize is the maximum page size Workday's list endpoint
	// accepts; larger values return an HTTP 400 (verified live during
	// planning against NVIDIA's board).
	workdayPageSize = 20
	// workdayMaxResults is a safety cap against runaway pagination on
	// unexpectedly huge boards.
	workdayMaxResults = 2000
	// workdayDetailWorkers bounds how many concurrent per-posting detail
	// requests we make (the list endpoint doesn't include a job
	// description - see fetchDetail).
	workdayDetailWorkers = 8
)

// Workday fetches postings from a company's Workday-hosted careers site.
// Workday has no single well-known API host, but the request shape is
// the same for every Workday customer:
//
//	POST https://{host}/wday/cxs/{tenant}/{site}/jobs
//	  {"appliedFacets":{},"limit":20,"offset":0,"searchText":""}
//
// (verified live during planning against NVIDIA's real career site). To
// onboard a new Workday company, open its real careers page with browser
// devtools (Network tab), search for any job title, and find the request
// to a URL matching the pattern above:
//   - Host   = the full hostname (e.g. "atlassian.wd3.myworkdayjobs.com")
//   - Tenant = the path segment right after /wday/cxs/ (e.g. "atlassian")
//   - Site   = the path segment after that (e.g. "Atlassian") - case-sensitive
type Workday struct {
	Tenant      string
	Host        string
	Site        string
	DisplayName string

	HTTPClient       *http.Client
	MaxResponseBytes int64
}

// NewWorkday builds a Workday source for the given tenant/host/site and
// human-readable display name.
func NewWorkday(tenant, host, site, displayName string) *Workday {
	return &Workday{
		Tenant:           tenant,
		Host:             host,
		Site:             site,
		DisplayName:      displayName,
		HTTPClient:       defaultHTTPClient(),
		MaxResponseBytes: httpbody.MaxJSONBytes,
	}
}

func (w *Workday) Name() string { return "workday" }

func (w *Workday) jobsURL() (string, error) {
	return buildURL("https://"+w.Host, "", "wday", "cxs", w.Tenant, w.Site, "jobs")
}

// detailURL appends an untrusted path from the list response to this
// tenant's fixed API prefix. externalPath is normalized by
// sanitizePathSuffix rather than escaped as a single segment, because
// Workday's real values are multi-segment paths (e.g.
// "/job/Berlin-Engineer_JR2017846") whose separators must be preserved.
func (w *Workday) detailURL(externalPath string) (string, error) {
	base, err := buildURL("https://"+w.Host, "", "wday", "cxs", w.Tenant, w.Site)
	if err != nil {
		return "", err
	}
	return base + sanitizePathSuffix(externalPath), nil
}

type workdayListRequest struct {
	AppliedFacets map[string]any `json:"appliedFacets"`
	Limit         int            `json:"limit"`
	Offset        int            `json:"offset"`
	SearchText    string         `json:"searchText"`
}

type workdayListResponse struct {
	Total       int                  `json:"total"`
	JobPostings []workdayListPosting `json:"jobPostings"`
}

type workdayListPosting struct {
	Title         string   `json:"title"`
	ExternalPath  string   `json:"externalPath"`
	LocationsText string   `json:"locationsText"`
	BulletFields  []string `json:"bulletFields"`
}

// requisitionID returns a stable identifier for this posting. Workday's
// list endpoint conveniently includes the requisition ID (e.g.
// "JR2017846") in bulletFields, so a stable dedup key is available
// without needing the detail fetch to succeed first.
func (p workdayListPosting) requisitionID() string {
	if len(p.BulletFields) > 0 && p.BulletFields[0] != "" {
		return p.BulletFields[0]
	}
	return p.ExternalPath
}

type workdayDetailResponse struct {
	JobPostingInfo workdayJobPostingInfo `json:"jobPostingInfo"`
}

type workdayJobPostingInfo struct {
	JobDescription string `json:"jobDescription"`
	ExternalURL    string `json:"externalUrl"`
}

// Fetch retrieves every open posting on this company's Workday board,
// enriched with the description and canonical URL from each posting's
// detail endpoint. Individual detail-fetch failures are skipped (not
// fatal) so one bad posting can't sink an entire run.
func (w *Workday) Fetch(ctx context.Context) ([]model.Job, error) {
	postings, err := w.fetchList(ctx)
	if err != nil {
		return nil, err
	}

	results := make([]*model.Job, len(postings))
	forEachBounded(ctx, len(postings), workdayDetailWorkers, func(i int) {
		job, err := w.fetchDetail(ctx, postings[i])
		if err != nil {
			return
		}
		results[i] = job
	})

	jobs := make([]model.Job, 0, len(postings))
	for _, j := range results {
		if j != nil {
			jobs = append(jobs, *j)
		}
	}
	return jobs, nil
}

func (w *Workday) fetchList(ctx context.Context) ([]workdayListPosting, error) {
	var all []workdayListPosting
	offset := 0

	for {
		bodyBytes, err := json.Marshal(workdayListRequest{
			AppliedFacets: map[string]any{},
			Limit:         workdayPageSize,
			Offset:        offset,
			SearchText:    "",
		})
		if err != nil {
			return nil, fmt.Errorf("workday(%s): encoding list request: %w", w.Tenant, err)
		}

		url, err := w.jobsURL()
		if err != nil {
			return nil, fmt.Errorf("workday(%s): %w", w.Tenant, err)
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("workday(%s): building list request: %w", w.Tenant, err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := w.HTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("workday(%s): list request failed: %w", w.Tenant, err)
		}

		var parsed workdayListResponse
		decErr := httpbody.DecodeJSON(resp.Body, w.MaxResponseBytes, &parsed)
		status := resp.StatusCode
		_ = resp.Body.Close()

		if status != http.StatusOK {
			return nil, fmt.Errorf("workday(%s): unexpected status %d", w.Tenant, status)
		}
		if decErr != nil {
			return nil, fmt.Errorf("workday(%s): reading list response: %w", w.Tenant, decErr)
		}

		all = append(all, parsed.JobPostings...)
		offset += len(parsed.JobPostings)

		if len(parsed.JobPostings) == 0 || offset >= parsed.Total || offset >= workdayMaxResults {
			break
		}
	}

	return all, nil
}

func (w *Workday) fetchDetail(ctx context.Context, posting workdayListPosting) (*model.Job, error) {
	detailURL, err := w.detailURL(posting.ExternalPath)
	if err != nil {
		return nil, fmt.Errorf("workday(%s): %w", w.Tenant, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, detailURL, nil)
	if err != nil {
		return nil, fmt.Errorf("workday(%s): building detail request for %s: %w", w.Tenant, posting.ExternalPath, err)
	}

	resp, err := w.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workday(%s): detail request failed for %s: %w", w.Tenant, posting.ExternalPath, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("workday(%s): unexpected detail status %d for %s", w.Tenant, resp.StatusCode, posting.ExternalPath)
	}

	var detail workdayDetailResponse
	if err := httpbody.DecodeJSON(resp.Body, w.MaxResponseBytes, &detail); err != nil {
		return nil, fmt.Errorf("workday(%s): reading detail for %s: %w", w.Tenant, posting.ExternalPath, err)
	}

	url := detail.JobPostingInfo.ExternalURL
	if url == "" {
		// Fallback when the detail response omits the canonical URL: rebuild
		// it from the same sanitized path used for the request.
		base, err := buildURL("https://"+w.Host, "", w.Site)
		if err != nil {
			return nil, fmt.Errorf("workday(%s): %w", w.Tenant, err)
		}
		url = base + sanitizePathSuffix(posting.ExternalPath)
	}

	return &model.Job{
		ID:          fmt.Sprintf("workday:%s:%s", w.Tenant, posting.requisitionID()),
		Source:      w.Name(),
		Company:     w.DisplayName,
		Title:       posting.Title,
		Location:    posting.LocationsText,
		URL:         url,
		Description: stripHTML(detail.JobPostingInfo.JobDescription),
		// PostedAt is intentionally left zero: Workday only exposes a
		// relative string ("Posted Yesterday") rather than an absolute
		// timestamp, so we rely on the store's first_seen_at instead.
	}, nil
}
