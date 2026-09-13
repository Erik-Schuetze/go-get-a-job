// Package httpclient builds the http.Client used for every outbound request
// this program makes to a third party.
//
// Everything go-get-a-job fetches belongs to somebody else: the public job
// board APIs of companies the operator has no relationship with, and their
// careers pages. Those endpoints are free, unauthenticated, and documented
// for browser use - not for a polling client - so the tool is a guest on
// somebody else's infrastructure and has to behave like one. Three rules are
// enforced here rather than left to each caller:
//
//  1. Every request identifies the tool. An operator who objects to being
//     polled can then block or contact it by name instead of seeing an
//     anonymous client and having to guess who it is.
//  2. Requests are spaced out, so a single run cannot arrive as a burst.
//     The connectors legitimately fan out (a Workday or SmartRecruiters
//     board needs one detail request per posting), and without a floor on
//     the interval that fan-out is indistinguishable from a small flood.
//  3. A 429 or 503 is obeyed rather than repeated at speed. Being told to
//     slow down is information, and the cheapest possible response is to
//     back off for as long as the server asked.
//
// Scraping an HTML careers page is not itself a problem - some employers
// only publish jobs that way - but it has to happen through this client, so
// that a page scrape inherits the same identity, pacing, and backoff as an
// API call.
package httpclient

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// UserAgent identifies this tool to the operators of the APIs it polls.
//
// It is deliberately a constant rather than a config field: a configurable
// User-Agent is a User-Agent that can be set to a browser's, and pretending
// to be a browser is precisely the behaviour that makes automated clients
// unwelcome. The version-less form matches what most crawler policies
// expect (a product token plus a URL that explains it).
const UserAgent = "go-get-a-job (+https://github.com/Erik-Schuetze/go-get-a-job)"

// Defaults for the outbound client.
const (
	// DefaultTimeout bounds one request. Boards answer in milliseconds;
	// a hung connection is a failure, not something to wait out.
	DefaultTimeout = 30 * time.Second

	// DefaultMinInterval is the floor between two outbound requests,
	// measured from the start of one to the start of the next. At four
	// requests per second a full run stays well below anything an ATS
	// would consider abusive, while a daily run over dozens of boards
	// still finishes in minutes. It is a global floor rather than a
	// per-host one on purpose: the budget is small enough that being
	// conservative costs nothing, and a single counter is far easier to
	// reason about than a map of per-host timers.
	DefaultMinInterval = 250 * time.Millisecond

	// DefaultMaxRequests caps one process's lifetime outbound requests.
	// It exists to convert a pagination bug - the one failure mode that
	// can turn a polite client into a hostile one without anyone editing
	// anything - into a loud error instead of a runaway loop.
	DefaultMaxRequests = 10_000

	// DefaultMaxAttempts is how many times a request may be sent in total
	// when the server asks us to back off. Three attempts (the first plus
	// two retries) is enough to ride out a per-second rate limit without
	// turning a persistently throttling server into a long stall.
	DefaultMaxAttempts = 3

	// maxRetryWait bounds how long a single backoff may last. A server is
	// entitled to answer Retry-After: 3600, but a daily CronJob should not
	// sit on it: giving up and reporting the throttle is more useful than
	// blocking the run, and the next run will try again anyway.
	maxRetryWait = 30 * time.Second
)

// ErrRequestBudgetExceeded is returned once a run has made MaxRequests
// outbound requests. It is a bug signal (a pagination loop, a board
// advertising an implausible total) rather than a transient error, so it is
// reported rather than retried.
var ErrRequestBudgetExceeded = errors.New("outbound request budget exceeded")

// New returns an http.Client that identifies itself, paces its requests, and
// backs off when a server asks it to. Pass zero for any bound to use the
// default.
//
// Pacing is layered inside retrying, so a retry is paced like any other
// request: no code path can send two requests closer together than
// MinInterval, including the backoff path.
func New(minInterval time.Duration, maxRequests int) *http.Client {
	if minInterval <= 0 {
		minInterval = DefaultMinInterval
	}
	if maxRequests <= 0 {
		maxRequests = DefaultMaxRequests
	}
	return &http.Client{
		Timeout: DefaultTimeout,
		Transport: &Retry{
			Base: &Transport{
				Base:        http.DefaultTransport,
				MinInterval: minInterval,
				MaxRequests: maxRequests,
			},
			MaxAttempts: DefaultMaxAttempts,
		},
	}
}

// Retry is an http.RoundTripper that retries a request which the server
// answered with a rate-limit or temporary-unavailable status, waiting as long
// as the response's Retry-After header asked it to (bounded by maxRetryWait).
//
// Only 429 and 503 are retried. Both are the server explicitly saying "not
// now, later", which is safe to obey. A 500 is not retried: it usually means
// the request itself was wrong, and repeating it is noise rather than
// patience.
type Retry struct {
	// Base performs the actual requests. Required.
	Base http.RoundTripper

	// MaxAttempts is the total number of sends per request, including the
	// first. Values below 1 are treated as 1 (no retries).
	MaxAttempts int
}

func (t *Retry) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

// RoundTrip implements http.RoundTripper.
func (t *Retry) RoundTrip(req *http.Request) (*http.Response, error) {
	attempts := t.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}

	var (
		resp *http.Response
		err  error
	)
	for attempt := 0; attempt < attempts; attempt++ {
		resp, err = t.base().RoundTrip(req)
		if err != nil {
			return nil, err
		}
		if !retryable(resp.StatusCode) || attempt == attempts-1 {
			return resp, nil
		}

		wait := backoff(resp, attempt)
		// Drain and close before retrying, so the connection can be
		// reused rather than left half-read.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDrainBytes))
		_ = resp.Body.Close()

		select {
		case <-time.After(wait):
		case <-req.Context().Done():
			return nil, req.Context().Err()
		}

		// A retried request needs a fresh body. NewRequest sets GetBody for
		// the in-memory readers every connector uses; when it is absent
		// (an unrewindable stream) retrying would send an empty body, so
		// the original response is returned instead of a wrong request.
		if req.Body != nil {
			if req.GetBody == nil {
				return nil, errNotRetryable
			}
			body, err := req.GetBody()
			if err != nil {
				return nil, fmt.Errorf("rewinding request body for retry: %w", err)
			}
			req.Body = body
		}
	}
	return resp, nil
}

// errNotRetryable reports that a request could not be retried because its
// body cannot be replayed. It is a programming error in the caller, not a
// server condition.
var errNotRetryable = errors.New("request body cannot be replayed, so the throttled request was not retried")

// maxDrainBytes bounds how much of a discarded response body is read to free
// the connection for reuse.
const maxDrainBytes = 64 << 10

// retryable reports whether a status is the server asking for patience
// rather than reporting a real failure.
func retryable(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable
}

// backoff computes how long to wait before the next attempt: the server's
// own Retry-After when it gave a usable one, otherwise increasing delays.
// Either way the result is clamped to maxRetryWait.
func backoff(resp *http.Response, attempt int) time.Duration {
	// attempt is 0-based, so the first retry waits one base delay.
	fallback := time.Duration(1<<attempt) * time.Second
	if resp == nil {
		return fallback
	}

	// Retry-After is either a number of seconds or an HTTP date.
	raw := resp.Header.Get("Retry-After")
	if raw == "" {
		return fallback
	}
	if secs, err := strconv.Atoi(raw); err == nil {
		if secs < 0 {
			return fallback
		}
		return min(time.Duration(secs)*time.Second, maxRetryWait)
	}
	if when, err := http.ParseTime(raw); err == nil {
		wait := time.Until(when)
		if wait <= 0 {
			return 0
		}
		return min(wait, maxRetryWait)
	}
	return fallback
}

// Transport is an http.RoundTripper that stamps an identifying User-Agent
// onto every request, enforces a minimum interval between requests, and
// stops the process once it has made too many.
//
// The pacing is applied across all hosts through one timer, so the client's
// total outbound rate is bounded no matter how many boards a single run
// touches or how many goroutines a connector fans out with.
type Transport struct {
	// Base is the transport that actually performs requests. Defaults to
	// http.DefaultTransport.
	Base http.RoundTripper

	// MinInterval is the minimum time between the starts of two requests.
	MinInterval time.Duration

	// MaxRequests caps the lifetime number of requests. Zero means no cap.
	MaxRequests int

	mu        sync.Mutex
	lastStart time.Time
	count     int
}

func (t *Transport) base() http.RoundTripper {
	if t.Base != nil {
		return t.Base
	}
	return http.DefaultTransport
}

// Requests reports how many requests have been made so far.
func (t *Transport) Requests() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.count
}

// RoundTrip implements http.RoundTripper. It waits out the remainder of the
// pacing interval, then delegates.
func (t *Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.acquire(req); err != nil {
		return nil, err
	}

	// Clone before mutating: a RoundTripper must not modify the request it
	// was handed, and the User-Agent is set rather than defaulted so the
	// Go runtime's own identifier can't leak out of a client built here.
	out := req.Clone(req.Context())
	out.Header.Set("User-Agent", UserAgent)

	return t.base().RoundTrip(out)
}

// acquire reserves one request from the budget and waits for the pacing
// interval. The wait happens while the lock is held, which serializes
// callers - that is the point, since it is what turns a concurrent fan-out
// into a spaced-out sequence of requests.
func (t *Transport) acquire(req *http.Request) error {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.MaxRequests > 0 && t.count >= t.MaxRequests {
		return fmt.Errorf("%w: %d requests made this run; refusing to make more", ErrRequestBudgetExceeded, t.count)
	}

	if t.MinInterval > 0 && !t.lastStart.IsZero() {
		wait := t.MinInterval - time.Since(t.lastStart)
		if wait > 0 {
			select {
			case <-time.After(wait):
			case <-req.Context().Done():
				return req.Context().Err()
			}
		}
	}

	t.lastStart = time.Now()
	t.count++
	return nil
}

var _ http.RoundTripper = (*Transport)(nil)
