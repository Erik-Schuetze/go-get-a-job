package httpclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTransport_SetsIdentifyingUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("User-Agent")
	}))
	defer srv.Close()

	c := &http.Client{Transport: &Transport{Base: srv.Client().Transport, MinInterval: time.Nanosecond}}
	resp, err := c.Get(srv.URL) //nolint:noctx // test server with no request context needed
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if got != UserAgent {
		t.Errorf("User-Agent = %q, want %q", got, UserAgent)
	}
}

// TestTransport_SpacesSequentialRequests guards the property the package
// exists for: a run must not arrive as a burst.
func TestTransport_SpacesSequentialRequests(t *testing.T) {
	const interval = 20 * time.Millisecond
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	c := &http.Client{Transport: &Transport{Base: srv.Client().Transport, MinInterval: interval}}

	start := time.Now()
	for i := 0; i < 3; i++ {
		resp, err := c.Get(srv.URL) //nolint:noctx // test server with no request context needed
		if err != nil {
			t.Fatalf("GET %d: %v", i, err)
		}
		_ = resp.Body.Close()
	}
	elapsed := time.Since(start)

	// Two gaps between three requests; allow a little slack for scheduling
	// without letting a real regression (no pacing at all) pass.
	if want := 2 * interval; elapsed < want {
		t.Errorf("3 requests took %v, want at least %v (interval %v)", elapsed, want, interval)
	}
}

// TestTransport_SerializesConcurrentRequests is the case that actually
// matters: the connectors fan out with bounded concurrency, so pacing has to
// hold across goroutines, not just within one loop.
func TestTransport_SerializesConcurrentRequests(t *testing.T) {
	const (
		interval = 20 * time.Millisecond
		n        = 5
	)
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	tr := &Transport{Base: srv.Client().Transport, MinInterval: interval}
	c := &http.Client{Transport: tr}

	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := c.Get(srv.URL) //nolint:noctx // test server with no request context needed
			if err != nil {
				t.Errorf("GET: %v", err)
				return
			}
			_ = resp.Body.Close()
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	if want := time.Duration(n-1) * interval; elapsed < want {
		t.Errorf("%d concurrent requests took %v, want at least %v - pacing did not hold across goroutines", n, elapsed, want)
	}
	if got := tr.Requests(); got != n {
		t.Errorf("Requests() = %d, want %d", got, n)
	}
}

// TestTransport_EnforcesRequestBudget covers the runaway-pagination guard.
func TestTransport_EnforcesRequestBudget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	tr := &Transport{Base: srv.Client().Transport, MinInterval: time.Nanosecond, MaxRequests: 2}
	c := &http.Client{Transport: tr}

	for i := 0; i < 2; i++ {
		resp, err := c.Get(srv.URL) //nolint:noctx // test server with no request context needed
		if err != nil {
			t.Fatalf("GET %d: %v", i, err)
		}
		_ = resp.Body.Close()
	}

	resp, err := c.Get(srv.URL) //nolint:noctx // test server with no request context needed
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrRequestBudgetExceeded) {
		t.Fatalf("third request: err = %v, want ErrRequestBudgetExceeded", err)
	}
	if got := tr.Requests(); got != 2 {
		t.Errorf("Requests() = %d, want 2 (the refused request must not count)", got)
	}
}

// TestTransport_DoesNotMutateCallerRequest checks the RoundTripper contract:
// the caller's request must come back untouched.
func TestTransport_DoesNotMutateCallerRequest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("User-Agent", "caller-supplied")

	tr := &Transport{Base: srv.Client().Transport, MinInterval: time.Nanosecond}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	if got := req.Header.Get("User-Agent"); got != "caller-supplied" {
		t.Errorf("caller's request was mutated: User-Agent = %q", got)
	}
}

func TestTransport_CancelledContextDuringPause(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()

	tr := &Transport{Base: srv.Client().Transport, MinInterval: 5 * time.Second}

	first, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building first request: %v", err)
	}
	resp, err := tr.RoundTrip(first)
	if err != nil {
		t.Fatalf("first RoundTrip: %v", err)
	}
	_ = resp.Body.Close()

	// The second request must not sit through the full 5s pause once its
	// context is cancelled.
	ctx, cancel := context.WithCancel(context.Background())
	second, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatalf("building second request: %v", err)
	}
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	resp2, err := tr.RoundTrip(second)
	if resp2 != nil {
		_ = resp2.Body.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("cancellation took %v - the pause ignored the request context", elapsed)
	}
}

func TestNew_AppliesDefaultsAndHonoursOverrides(t *testing.T) {
	c := New(0, 0)
	retry, ok := c.Transport.(*Retry)
	if !ok {
		t.Fatalf("Transport is %T, want *Retry", c.Transport)
	}
	tr, ok := retry.Base.(*Transport)
	if !ok {
		t.Fatalf("Retry.Base is %T, want *Transport", retry.Base)
	}
	if tr.MinInterval != DefaultMinInterval || tr.MaxRequests != DefaultMaxRequests {
		t.Errorf("New(0, 0) = interval %v / max %d, want defaults %v / %d",
			tr.MinInterval, tr.MaxRequests, DefaultMinInterval, DefaultMaxRequests)
	}
	if retry.MaxAttempts != DefaultMaxAttempts {
		t.Errorf("MaxAttempts = %d, want %d", retry.MaxAttempts, DefaultMaxAttempts)
	}
	if c.Timeout != DefaultTimeout {
		t.Errorf("Timeout = %v, want %v", c.Timeout, DefaultTimeout)
	}

	c = New(7*time.Second, 3)
	tr, ok = c.Transport.(*Retry).Base.(*Transport)
	if !ok {
		t.Fatalf("Transport is %T, want *Transport", c.Transport)
	}
	if tr.MinInterval != 7*time.Second || tr.MaxRequests != 3 {
		t.Errorf("New(7s, 3) = interval %v / max %d, want 7s / 3", tr.MinInterval, tr.MaxRequests)
	}
}

// --- Retry ---------------------------------------------------------------

// flakyServer answers the first failWith requests with status, then 200.
func flakyServer(failWith int, statuses ...int) (*httptest.Server, *int32) {
	var mu sync.Mutex
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		n := calls
		calls++
		mu.Unlock()
		if int(n) < len(statuses) {
			if ra := r.URL.Query().Get("retry_after"); ra != "" {
				w.Header().Set("Retry-After", ra)
			}
			w.WriteHeader(statuses[n])
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	_ = failWith
	return srv, &calls
}

func TestRetry_RetriesRateLimitAndHonoursRetryAfter(t *testing.T) {
	srv, calls := flakyServer(0, http.StatusTooManyRequests)
	defer srv.Close()

	tr := &Retry{Base: srv.Client().Transport, MaxAttempts: 3}
	c := &http.Client{Transport: tr}

	start := time.Now()
	resp, err := c.Get(srv.URL + "?retry_after=1") //nolint:noctx // test server with no request context needed
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200 after the retry", resp.StatusCode)
	}
	if *calls != 2 {
		t.Errorf("server saw %d requests, want 2", *calls)
	}
	// Retry-After: 1 must actually be waited out.
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("retry after %v, want at least the 1s Retry-After", elapsed)
	}
}

func TestRetry_GivesUpAfterMaxAttempts(t *testing.T) {
	srv, calls := flakyServer(0, http.StatusServiceUnavailable, http.StatusServiceUnavailable)
	defer srv.Close()

	// MinInterval 0 means the fallback backoff would be 1s; Retry-After: 0
	// keeps the test fast while still exercising the header path.
	tr := &Retry{Base: srv.Client().Transport, MaxAttempts: 2}
	c := &http.Client{Transport: tr}

	resp, err := c.Get(srv.URL + "?retry_after=0") //nolint:noctx // test server with no request context needed
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The caller gets the last response rather than a synthetic error - the
	// connector's own status check then reports it.
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want the final 503 handed back", resp.StatusCode)
	}
	if *calls != 2 {
		t.Errorf("server saw %d requests, want 2 (MaxAttempts)", *calls)
	}
}

func TestRetry_DoesNotRetryOtherStatuses(t *testing.T) {
	for _, status := range []int{http.StatusInternalServerError, http.StatusForbidden, http.StatusNotFound} {
		srv, calls := flakyServer(0, status)
		tr := &Retry{Base: srv.Client().Transport, MaxAttempts: 3}
		c := &http.Client{Transport: tr}

		resp, err := c.Get(srv.URL) //nolint:noctx // test server with no request context needed
		if err != nil {
			t.Fatalf("status %d: GET: %v", status, err)
		}
		_ = resp.Body.Close()

		if *calls != 1 {
			t.Errorf("status %d: server saw %d requests, want 1 (not retried)", status, *calls)
		}
		srv.Close()
	}
}

func TestBackoff_ClampsAndFallsBack(t *testing.T) {
	// A server is entitled to ask for longer than a run should wait.
	resp := &http.Response{Header: http.Header{"Retry-After": []string{"3600"}}}
	if got := backoff(resp, 0); got != maxRetryWait {
		t.Errorf("backoff for Retry-After: 3600 = %v, want clamp to %v", got, maxRetryWait)
	}

	resp = &http.Response{Header: http.Header{"Retry-After": []string{"2"}}}
	if got := backoff(resp, 0); got != 2*time.Second {
		t.Errorf("backoff for Retry-After: 2 = %v, want 2s", got)
	}

	// An HTTP-date in the past means "now".
	resp = &http.Response{Header: http.Header{"Retry-After": []string{time.Now().Add(-time.Minute).UTC().Format(http.TimeFormat)}}}
	if got := backoff(resp, 0); got != 0 {
		t.Errorf("backoff for a past HTTP date = %v, want 0", got)
	}

	// No header at all: exponential fallback, 1s then 2s.
	resp = &http.Response{Header: http.Header{}}
	if got := backoff(resp, 0); got != time.Second {
		t.Errorf("fallback backoff attempt 0 = %v, want 1s", got)
	}
	if got := backoff(resp, 1); got != 2*time.Second {
		t.Errorf("fallback backoff attempt 1 = %v, want 2s", got)
	}
}

// TestRetry_RewindsBodyForRetriedPost covers the request shape the Workday
// list endpoint uses: a POST that must be replayed byte-for-byte, not
// re-sent empty.
func TestRetry_RewindsBodyForRetriedPost(t *testing.T) {
	var (
		mu      sync.Mutex
		bodies  []string
		answers int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		n := answers
		answers++
		mu.Unlock()
		if n == 0 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tr := &Retry{Base: srv.Client().Transport, MaxAttempts: 3}
	c := &http.Client{Transport: tr}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL, strings.NewReader(`{"limit":20}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if len(bodies) != 2 {
		t.Fatalf("server saw %d requests, want 2", len(bodies))
	}
	if bodies[0] != `{"limit":20}` || bodies[1] != `{"limit":20}` {
		t.Errorf("bodies = %q, want the same JSON twice", bodies)
	}
}

// TestNew_PacesRetriesToo checks the layering: retries must not be exempt
// from the pacing rule.
func TestNew_PacesRetriesToo(t *testing.T) {
	const interval = 30 * time.Millisecond
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	client := New(interval, 100)
	client.Transport.(*Retry).Base.(*Transport).Base = srv.Client().Transport

	start := time.Now()
	resp, err := client.Get(srv.URL) //nolint:noctx // test server with no request context needed
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if elapsed := time.Since(start); elapsed < interval {
		t.Errorf("retry after %v, want at least the %v pacing interval", elapsed, interval)
	}
}
