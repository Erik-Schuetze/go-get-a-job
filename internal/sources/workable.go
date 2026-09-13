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

const workableDefaultBaseURL = "https://apply.workable.com/api/v1/widget/accounts"

// Workable fetches postings from a company's public Workable widget API:
// GET https://apply.workable.com/api/v1/widget/accounts/{company}?details=true
//
// This is the endpoint behind Workable's embeddable job widget, so it is
// public and unauthenticated by design. Verified live during planning against
// Hugging Face. An unknown account answers 404, so a mistyped slug is loud.
type Workable struct {
	Company     string
	DisplayName string

	BaseURL          string
	HTTPClient       *http.Client
	MaxResponseBytes int64
}

// NewWorkable builds a Workable source for the given account slug and
// human-readable display name.
func NewWorkable(company, displayName string) *Workable {
	return &Workable{
		Company:          company,
		DisplayName:      displayName,
		BaseURL:          workableDefaultBaseURL,
		HTTPClient:       defaultHTTPClient(),
		MaxResponseBytes: httpbody.MaxAPIBytes,
	}
}

func (w *Workable) Name() string { return "workable" }

func (w *Workable) Label() string { return "workable/" + w.Company }

type workableResponse struct {
	Name string        `json:"name"`
	Jobs []workableJob `json:"jobs"`
}

type workableJob struct {
	Shortcode      string `json:"shortcode"`
	Code           string `json:"code"`
	Title          string `json:"title"`
	City           string `json:"city"`
	State          string `json:"state"`
	Country        string `json:"country"`
	Telecommuting  bool   `json:"telecommuting"`
	URL            string `json:"url"`
	Shortlink      string `json:"shortlink"`
	Department     string `json:"department"`
	Description    string `json:"description"`
	Requirements   string `json:"requirements"`
	EmploymentType string `json:"employment_type"`
	CreatedAt      string `json:"created_at"`
	PublishedOn    string `json:"published_on"`
}

// Fetch retrieves every open posting on this company's Workable board.
func (w *Workable) Fetch(ctx context.Context) ([]model.Job, error) {
	url, err := buildURL(w.BaseURL, "details=true", w.Company)
	if err != nil {
		return nil, fmt.Errorf("workable(%s): %w", w.Company, err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("workable(%s): building request: %w", w.Company, err)
	}

	resp, err := w.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("workable(%s): request failed: %w", w.Company, err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("workable(%s): unexpected status %d", w.Company, resp.StatusCode)
	}

	var decoded workableResponse
	if err := httpbody.DecodeJSON(resp.Body, w.MaxResponseBytes, &decoded); err != nil {
		return nil, fmt.Errorf("workable(%s): reading response: %w", w.Company, err)
	}

	jobs := make([]model.Job, 0, len(decoded.Jobs))
	for _, j := range decoded.Jobs {
		if j.Title == "" {
			continue
		}
		id := j.Shortcode
		if id == "" {
			// Older boards only carry the numeric code; either is stable,
			// and dedup only needs one of them to be consistent.
			id = j.Code
		}
		if id == "" {
			continue
		}
		jobs = append(jobs, model.Job{
			ID:          fmt.Sprintf("workable:%s:%s", w.Company, id),
			Source:      w.Name(),
			Company:     w.DisplayName,
			Title:       j.Title,
			Location:    workableLocation(j),
			URL:         workableURL(j),
			Description: workableDescription(j),
			PostedAt:    workableTime(j.PublishedOn, j.CreatedAt),
		})
	}
	return jobs, nil
}

// workableLocation joins the place fields Workable splits a posting across,
// leading with the remote marker when the posting is flagged remote: "EMEA
// Remote" postings are frequently filed under their registered office city, so
// the marker is what the pre-filter needs in order to hand them to the scorer
// instead of dropping them as a foreign onsite role.
func workableLocation(j workableJob) string {
	parts := make([]string, 0, 4)
	if j.Telecommuting {
		parts = append(parts, "Remote")
	}
	for _, p := range []string{j.City, j.State, j.Country} {
		p = strings.TrimSpace(p)
		if p == "" || containsFold(parts, p) {
			continue
		}
		parts = append(parts, p)
	}
	return strings.Join(parts, ", ")
}

// workableURL prefers the canonical posting link and falls back to the
// shortlink, which for Workable are usually the same document.
func workableURL(j workableJob) string {
	if strings.TrimSpace(j.URL) != "" {
		return j.URL
	}
	return j.Shortlink
}

// workableDescription keeps the requirements section, which is where a
// platform or infrastructure role usually names its stack. Dropping it would
// mean scoring the intro paragraph alone.
func workableDescription(j workableJob) string {
	parts := make([]string, 0, 2)
	if d := stripHTML(j.Description); d != "" {
		parts = append(parts, d)
	}
	if r := stripHTML(j.Requirements); r != "" {
		parts = append(parts, "Requirements\n"+r)
	}
	return strings.Join(parts, "\n\n")
}

// workableTime parses the date-only timestamps Workable uses, preferring the
// publish date and falling back to the creation date. A date-only value is
// parsed as midnight UTC rather than left zero, since it is still a
// comparable instant.
func workableTime(values ...string) time.Time {
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if t, err := time.Parse("2006-01-02", v); err == nil {
			return t
		}
		if t := parseRFC3339Best(v); !t.IsZero() {
			return t
		}
	}
	return time.Time{}
}
