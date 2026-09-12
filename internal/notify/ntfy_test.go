package notify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

// captureServer records the last publish it received and replies with the
// given status code.
type capturedPublish struct {
	path    string
	headers http.Header
	body    string
}

func captureServer(t *testing.T, status int, respBody string) (*httptest.Server, *capturedPublish) {
	t.Helper()
	captured := &capturedPublish{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		captured.path = r.URL.EscapedPath()
		captured.headers = r.Header.Clone()
		captured.body = string(raw)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(respBody))
	}))
	t.Cleanup(server.Close)
	return server, captured
}

func TestNotify_RejectsNonHTTPClickHeader(t *testing.T) {
	cases := []struct {
		name string
		url  string
		want string
	}{
		{"https is kept", "https://boards.greenhouse.io/acme/jobs/123", "https://boards.greenhouse.io/acme/jobs/123"},
		{"http is kept", "http://example.com/job", "http://example.com/job"},
		{"javascript is dropped", "javascript:alert(1)", ""},
		{"data is dropped", "data:text/html,<script>alert(1)</script>", ""},
		{"file is dropped", "file:///etc/passwd", ""},
		{"relative is dropped", "/acme/jobs/123", ""},
		{"scheme-relative is dropped", "//evil.example.com/x", ""},
		{"empty is dropped", "", ""},
		{"whitespace is dropped", "   ", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server, captured := captureServer(t, http.StatusOK, "")
			n := NewNtfy(server.URL, "job-matches", "")
			n.HTTPClient = server.Client()

			job := model.Job{Title: "Engineer", Company: "Acme", URL: tc.url}
			if err := n.Notify(context.Background(), job, "matched"); err != nil {
				t.Fatalf("Notify returned error: %v", err)
			}

			got := captured.headers.Get("Click")
			if got != tc.want {
				t.Errorf("Click header = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNotify_StripsControlCharactersFromHeadersAndBody(t *testing.T) {
	server, captured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(server.URL, "job-matches", "")
	n.HTTPClient = server.Client()

	job := model.Job{
		Title:    "Senior\r\nEngineer\x1b]0;pwned\x07",
		Company:  "Ac\x00me",
		Location: "Remote\u202eevil",
		URL:      "https://example.com/job",
	}
	reason := "Great\r\nmatch\x1b[31m"

	if err := n.Notify(context.Background(), job, reason); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	// Headers are single-line by definition: a stray CR/LF there either
	// corrupts the request or gets the notification rejected outright.
	for name, value := range map[string]string{
		"Title": captured.headers.Get("Title"),
		"Tags":  captured.headers.Get("Tags"),
		"Click": captured.headers.Get("Click"),
	} {
		if strings.ContainsAny(value, "\x00\x07\x1b\r\n\u202e") {
			t.Errorf("%s still contains control characters: %q", name, value)
		}
	}

	// The body is deliberately multi-line, so newlines are legitimate there;
	// the terminal and bidi escapes are not.
	if strings.ContainsAny(captured.body, "\x00\x07\x1b\r\u202e") {
		t.Errorf("body still contains control characters: %q", captured.body)
	}
	if !strings.Contains(captured.body, "Great") || !strings.Contains(captured.body, "match") {
		t.Errorf("expected the reason to survive sanitization in readable form, got %q", captured.body)
	}
	if !strings.Contains(captured.headers.Get("Title"), "Acme: Senior Engineer") {
		t.Errorf("expected the title header to keep its readable content, got %q", captured.headers.Get("Title"))
	}
}

func TestNotify_CapsHeaderAndBodyLength(t *testing.T) {
	server, captured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(server.URL, "job-matches", "")
	n.HTTPClient = server.Client()

	job := model.Job{
		Title:    strings.Repeat("t", 1000),
		Company:  strings.Repeat("c", 1000),
		Location: strings.Repeat("l", 1000),
	}

	if err := n.Notify(context.Background(), job, strings.Repeat("r", 5000)); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if got := len([]rune(captured.headers.Get("Title"))); got > maxTitleChars+3 {
		t.Errorf("Title header is %d runes, want at most %d", got, maxTitleChars+3)
	}
	if got := len([]rune(captured.body)); got > maxBodyChars+3 {
		t.Errorf("body is %d runes, want at most %d", got, maxBodyChars+3)
	}
	if strings.Contains(captured.body, strings.Repeat("l", 200)) {
		t.Error("expected the location to be capped")
	}
}

func TestNotify_FallsBackToGenericBodyWhenReasonEmpty(t *testing.T) {
	server, captured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(server.URL, "job-matches", "")
	n.HTTPClient = server.Client()

	job := model.Job{Title: "Engineer", Company: "Acme"}
	if err := n.Notify(context.Background(), job, ""); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if !strings.Contains(captured.body, "New match: Engineer at Acme") {
		t.Errorf("expected the generic fallback body, got %q", captured.body)
	}
}

// The configured topic is interpolated into the request target, so it is
// escaped rather than pasted: on the wire it stays inside one path segment
// and cannot introduce a query string. (Config validation already restricts
// the topic to a safe charset; this is the second layer, for the case where
// a value reaches the notifier by some other route.)
func TestNotify_EscapesTopicIntoSinglePathSegment(t *testing.T) {
	server, captured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(server.URL, "topic/../../v1/health?x=1", "")
	n.HTTPClient = server.Client()

	if err := n.Notify(context.Background(), model.Job{Title: "x"}, "y"); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if strings.Count(captured.path, "/") != 1 {
		t.Errorf("expected the topic to occupy a single path segment, got %q", captured.path)
	}
	if strings.ContainsAny(captured.path, "?#") {
		t.Errorf("expected no query or fragment in the request target, got %q", captured.path)
	}
}

func TestNotify_TrimsTrailingSlashFromBaseURL(t *testing.T) {
	n := NewNtfy("https://ntfy.example.com/", "job-matches", "")
	if n.URL != "https://ntfy.example.com" {
		t.Errorf("URL = %q, want the trailing slash trimmed", n.URL)
	}
}

func TestNotify_SetsBearerTokenWhenConfigured(t *testing.T) {
	server, captured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(server.URL, "job-matches", "tk_secret")
	n.HTTPClient = server.Client()

	if err := n.Notify(context.Background(), model.Job{Title: "x"}, "y"); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if got := captured.headers.Get("Authorization"); got != "Bearer tk_secret" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer tk_secret")
	}
}

func TestNotify_OmitsAuthorizationWhenNoToken(t *testing.T) {
	server, captured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(server.URL, "job-matches", "")
	n.HTTPClient = server.Client()

	if err := n.Notify(context.Background(), model.Job{Title: "x"}, "y"); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if got := captured.headers.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want it unset", got)
	}
}

func TestNotify_ReportsNon2xxWithBoundedErrorBody(t *testing.T) {
	server, _ := captureServer(t, http.StatusUnauthorized, strings.Repeat("e", 10000))
	n := NewNtfy(server.URL, "job-matches", "bad")
	n.HTTPClient = server.Client()
	n.MaxErrorBytes = 64

	err := n.Notify(context.Background(), model.Job{Title: "x"}, "y")
	if err == nil {
		t.Fatal("expected an error for a 401 response")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected the status to be reported, got %v", err)
	}
	if len(err.Error()) > 1000 {
		t.Errorf("expected the error body to be bounded, got %d bytes", len(err.Error()))
	}
}

func TestNotifyFailure_SanitizesAndCapsTheErrorMessage(t *testing.T) {
	server, captured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(server.URL, "job-matches", "")
	n.HTTPClient = server.Client()

	runErr := errors.New("greenhouse: unexpected status 500: \x1b]0;pwned\x07" + strings.Repeat("x", 5000))
	if err := n.NotifyFailure(context.Background(), runErr); err != nil {
		t.Fatalf("NotifyFailure returned error: %v", err)
	}

	if got := captured.headers.Get("Priority"); got != "high" {
		t.Errorf("Priority = %q, want %q", got, "high")
	}
	if strings.ContainsAny(captured.body, "\x1b\x07") {
		t.Errorf("expected control characters to be stripped from the body, got %q", captured.body)
	}
	if got := len([]rune(captured.body)); got > maxBodyChars+3 {
		t.Errorf("body is %d runes, want at most %d", got, maxBodyChars+3)
	}
}

func TestNotify_PropagatesTransportError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := server.URL
	server.Close() // nothing is listening any more

	n := NewNtfy(url, "job-matches", "")
	if err := n.Notify(context.Background(), model.Job{Title: "x"}, "y"); err == nil {
		t.Fatal("expected a transport error")
	}
}
