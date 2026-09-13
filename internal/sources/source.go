// Package sources implements one connector per supported ATS (Applicant
// Tracking System): Greenhouse, Lever, Ashby, SmartRecruiters, and a
// generic Workday connector. Each connector normalizes its provider's
// response shape into []model.Job so the rest of the pipeline never has
// to care which board a posting came from.
package sources

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Erik-Schuetze/go-get-a-job/internal/httpclient"
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
	// Every connector gets the same client: identified, paced, and with a
	// bounded request budget. See internal/httpclient for why each of those
	// is not optional when the hosts being fetched are other people's.
	return httpclient.New(httpclient.DefaultMinInterval, httpclient.DefaultMaxRequests)
}

// buildURL joins a request URL out of a trusted base and untrusted path
// segments, percent-encoding each segment.
//
// Every connector interpolates a config-derived slug (a board token, tenant,
// site, or company identifier) into a path, and two of them also interpolate
// a value that came back from the previous API response. Concatenating those
// with fmt.Sprintf, as this used to do, means a value containing "/", "?",
// or "#" silently rewrites the request: a "/" adds path segments, and a "?"
// replaces the query string the caller thought it was sending. Encoding each
// segment independently makes a hostile value collide-proof, since nothing
// it contains can be re-read as URL structure.
//
// The base must be an absolute URL with a host and no query or fragment of
// its own; rawQuery, if non-empty, is appended verbatim and must therefore
// be a trusted literal built by the caller, not untrusted input.
func buildURL(base, rawQuery string, segments ...string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid base URL %q: %w", base, err)
	}
	if !u.IsAbs() || u.Host == "" {
		return "", fmt.Errorf("base URL %q must be absolute (scheme://host)", base)
	}
	if u.User != nil {
		return "", fmt.Errorf("base URL %q must not contain credentials", base)
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("base URL %q must not contain a query or fragment", base)
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(u.Scheme+"://"+u.Host+u.EscapedPath(), "/"))
	for _, s := range segments {
		if s == "" {
			return "", fmt.Errorf("empty path segment in URL under %q", base)
		}
		b.WriteString("/")
		b.WriteString(url.PathEscape(s))
	}
	if rawQuery != "" {
		b.WriteString("?")
		b.WriteString(rawQuery)
	}
	return b.String(), nil
}

// sanitizePathSuffix normalizes an untrusted value that is expected to be a
// path on an already-fixed host, such as Workday's externalPath. It returns a
// value starting with "/" (or "" if nothing usable was left) that can be
// appended directly to a URL prefix.
//
// It defends against the same class of value as buildURL but for a
// different shape of input. The value is a path rather than a single
// segment, so its "/" separators have to survive - which means each of the
// three ways a path can still carry structure is handled explicitly:
//
//   - an absolute URL where a path was expected has its scheme and host
//     dropped, so it can't silently redirect the request to another origin;
//   - a query or fragment is truncated, so it can't replace the one the
//     caller is sending;
//   - ".." segments are resolved against "/" so they can't climb out of the
//     prefix they get appended to. Percent-encoding does not smuggle one
//     past this: the value is decoded before it is cleaned, so "%2e%2e%2f"
//     is resolved to "/" rather than passed through as a literal. The
//     guarantee a caller gets is therefore not "the path is unchanged" but
//     "the result is a clean, rooted path on the origin the caller fixed".
func sanitizePathSuffix(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}

	// Parsing separates a path from its scheme/host (when an absolute URL was
	// supplied where a path was expected) and from any query or fragment, so
	// only the path component is ever kept.
	if u, err := url.Parse(s); err == nil {
		s = u.Path
	}
	// Belt-and-braces for a value that failed to parse at all.
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		s = s[:i]
	}

	// EscapedPath re-escapes control characters, spaces, "?", and "#" - the
	// things that would otherwise still carry structure - while leaving the
	// "/" separators a path needs intact.
	escaped := (&url.URL{Path: s}).EscapedPath()

	// Clean rooted at "/" collapses any ".." that survived, so the result
	// can never point above the prefix it is appended to.
	cleaned := path.Clean("/" + strings.TrimLeft(escaped, "/"))
	if cleaned == "/" || cleaned == "." {
		return ""
	}
	return cleaned
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
