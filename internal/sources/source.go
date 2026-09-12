// Package sources implements one connector per supported ATS (Applicant
// Tracking System): Greenhouse, Lever, Ashby, SmartRecruiters, and a
// generic Workday connector. Each connector normalizes its provider's
// response shape into []model.Job so the rest of the pipeline never has
// to care which board a posting came from.
package sources

import (
	"context"
	"html"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

// Source fetches all currently open job postings from one company's ATS.
// Implementations should isolate their own transient, per-item failures
// internally where reasonable (e.g. skip one bad detail fetch and log it)
// and only return an error when the fetch as a whole is unusable (e.g. the
// list endpoint itself failed).
type Source interface {
	// Name identifies the connector type (e.g. "greenhouse"); it's used to
	// tag each returned Job's Source field and in logs.
	Name() string

	// Fetch returns all currently open postings for this source.
	Fetch(ctx context.Context) ([]model.Job, error)
}

func defaultHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}

var (
	htmlBlockBreakRE = regexp.MustCompile(`(?i)</p>|<br\s*/?>|</li>|</div>|</h[1-6]>`)
	htmlTagRE        = regexp.MustCompile(`<[^>]*>`)
	repeatedSpaceRE  = regexp.MustCompile(`[ \t\f\v]+`)
	repeatedBlankRE  = regexp.MustCompile(`\n{3,}`)
)

// stripHTML does a best-effort conversion of an HTML fragment (as returned
// by several ATS APIs for job descriptions) into readable plain text. It's
// intentionally simple: it's used to feed keyword matching and the AI
// prompt, not to render markup, so perfect fidelity isn't required.
func stripHTML(s string) string {
	if s == "" {
		return ""
	}
	s = htmlBlockBreakRE.ReplaceAllString(s, "\n")
	s = htmlTagRE.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = repeatedSpaceRE.ReplaceAllString(s, " ")
	s = repeatedBlankRE.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// parseRFC3339Best parses an RFC3339 timestamp (with or without fractional
// seconds), returning the zero time on failure rather than an error -
// PostedAt is best-effort across sources and should never fail a fetch.
func parseRFC3339Best(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// msToTime converts a Unix epoch-milliseconds value (as used by Lever's
// createdAt) to a time.Time. Zero returns the zero time.
func msToTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// forEachBounded calls fn(i) for every i in [0,n) using up to maxWorkers
// goroutines at a time, stopping early (best-effort) if ctx is cancelled.
// It does not fail fast on individual fn calls - callers are expected to
// handle/log per-item errors inside fn so one bad item never aborts an
// entire batch. Used by connectors that need a per-job detail fetch
// (Workday, SmartRecruiters) without hammering the upstream API with an
// unbounded number of concurrent requests.
func forEachBounded(ctx context.Context, n, maxWorkers int, fn func(i int)) {
	if n == 0 {
		return
	}
	if maxWorkers < 1 {
		maxWorkers = 1
	}
	if maxWorkers > n {
		maxWorkers = n
	}

	sem := make(chan struct{}, maxWorkers)
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}
			fn(i)
		}(i)
	}
	wg.Wait()
}
