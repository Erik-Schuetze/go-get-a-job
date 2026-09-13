package notify

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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
			n := NewNtfy(server.URL, "job-matches", "", nil)
			n.HTTPClient = server.Client()

			job := model.Job{Title: "Engineer", Company: "Acme", URL: tc.url}
			if err := n.Notify(context.Background(), Match{Job: job, Reason: "matched"}); err != nil {
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
	n := NewNtfy(server.URL, "job-matches", "", nil)
	n.HTTPClient = server.Client()

	job := model.Job{
		Title:    "Senior\r\nEngineer\x1b]0;pwned\x07",
		Company:  "Ac\x00me",
		Location: "Remote\u202eevil",
		URL:      "https://example.com/job",
	}
	reason := "Great\r\nmatch\x1b[31m"

	if err := n.Notify(context.Background(), Match{Job: job, Reason: reason}); err != nil {
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
	n := NewNtfy(server.URL, "job-matches", "", nil)
	n.HTTPClient = server.Client()

	job := model.Job{
		Title:    strings.Repeat("t", 1000),
		Company:  strings.Repeat("c", 1000),
		Location: strings.Repeat("l", 1000),
	}

	if err := n.Notify(context.Background(), Match{Job: job, Reason: strings.Repeat("r", 5000)}); err != nil {
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
	n := NewNtfy(server.URL, "job-matches", "", nil)
	n.HTTPClient = server.Client()

	job := model.Job{Title: "Engineer", Company: "Acme"}
	if err := n.Notify(context.Background(), Match{Job: job, Reason: ""}); err != nil {
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
	n := NewNtfy(server.URL, "topic/../../v1/health?x=1", "", nil)
	n.HTTPClient = server.Client()

	if err := n.Notify(context.Background(), Match{Job: model.Job{Title: "x"}, Reason: "y"}); err != nil {
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
	n := NewNtfy("https://ntfy.example.com/", "job-matches", "", nil)
	if n.URL != "https://ntfy.example.com" {
		t.Errorf("URL = %q, want the trailing slash trimmed", n.URL)
	}
}

func TestNotify_SetsBearerTokenWhenConfigured(t *testing.T) {
	server, captured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(server.URL, "job-matches", "tk_secret", nil)
	n.HTTPClient = server.Client()

	if err := n.Notify(context.Background(), Match{Job: model.Job{Title: "x"}, Reason: "y"}); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if got := captured.headers.Get("Authorization"); got != "Bearer tk_secret" {
		t.Errorf("Authorization = %q, want %q", got, "Bearer tk_secret")
	}
}

func TestNotify_OmitsAuthorizationWhenNoToken(t *testing.T) {
	server, captured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(server.URL, "job-matches", "", nil)
	n.HTTPClient = server.Client()

	if err := n.Notify(context.Background(), Match{Job: model.Job{Title: "x"}, Reason: "y"}); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if got := captured.headers.Get("Authorization"); got != "" {
		t.Errorf("Authorization = %q, want it unset", got)
	}
}

func TestNotify_ReportsNon2xxWithBoundedErrorBody(t *testing.T) {
	server, _ := captureServer(t, http.StatusUnauthorized, strings.Repeat("e", 10000))
	n := NewNtfy(server.URL, "job-matches", "bad", nil)
	n.HTTPClient = server.Client()
	n.MaxErrorBytes = 64

	err := n.Notify(context.Background(), Match{Job: model.Job{Title: "x"}, Reason: "y"})
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
	n := NewNtfy(server.URL, "job-matches", "", nil)
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

	n := NewNtfy(url, "job-matches", "", nil)
	if err := n.Notify(context.Background(), Match{Job: model.Job{Title: "x"}, Reason: "y"}); err == nil {
		t.Fatal("expected a transport error")
	}
}

// TestNotifyWarning_IsDistinguishableFromAJobMatch pins the one property the
// whole warning exists for: at a glance, from the notification list alone, the
// operator must be able to tell "a board went quiet" from "here is a job worth
// applying to". The title is the only field a phone is guaranteed to show, so
// the marker has to be in it - a tag or priority can be dropped by a client or
// hidden behind an expand.
func TestNotifyWarning_IsDistinguishableFromAJobMatch(t *testing.T) {
	warningServer, warningCaptured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(warningServer.URL, "job-matches", "", nil)
	n.HTTPClient = warningServer.Client()

	if err := n.NotifyWarning(context.Background(), "2 source(s) with no postings", "greenhouse/dead"); err != nil {
		t.Fatalf("NotifyWarning returned error: %v", err)
	}

	matchServer, matchCaptured := captureServer(t, http.StatusOK, "")
	m := NewNtfy(matchServer.URL, "job-matches", "", nil)
	m.HTTPClient = matchServer.Client()
	if err := m.Notify(context.Background(), Match{Job: model.Job{Title: "Platform Engineer", Company: "Acme"}, Reason: "great fit"}); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	warnTitle := warningCaptured.headers.Get("Title")
	if !strings.Contains(warnTitle, "warning") {
		t.Errorf("warning Title = %q, want it to contain %q", warnTitle, "warning")
	}
	if warnTitle == matchCaptured.headers.Get("Title") {
		t.Fatalf("warning and match titles are identical (%q)", warnTitle)
	}
	if got := warningCaptured.headers.Get("Tags"); !strings.Contains(got, "warning") {
		t.Errorf("Tags = %q, want it to contain %q", got, "warning")
	}

	// The body carries third-party text (source labels come from config, but
	// the same path will carry anything the guard adds later), so the same
	// control-character stripping as every other notification must apply.
	if err := n.NotifyWarning(context.Background(), "title\x1b]0;pwned\x07", strings.Repeat("x", 5000)); err != nil {
		t.Fatalf("NotifyWarning returned error: %v", err)
	}
	if strings.ContainsAny(warningCaptured.body, "\x1b\x07") {
		t.Errorf("expected control characters to be stripped from the body, got %q", warningCaptured.body)
	}
	if got := len([]rune(warningCaptured.body)); got > maxBodyChars+3 {
		t.Errorf("body is %d runes, want at most %d (+3 for the truncation marker)", got, maxBodyChars)
	}
	if got := len([]rune(warningCaptured.headers.Get("Title"))); got > maxTitleChars {
		t.Errorf("Title is %d runes, want at most %d", got, maxTitleChars)
	}
}

// TestNotify_TierEmojiAndCompanyTagReplaceTheBriefcaseTag pins the two halves
// of the layout the notification list is read from: the emoji carries the
// score, and the tag carries the company.
//
// The tag is asserted absent by name because of how ntfy renders tags: a tag
// matching an emoji short code is turned into an emoji *prepended to the
// title*, so a stray "briefcase" tag would put 💼 next to the tier emoji on
// every single notification. The check is on the header value rather
// than on the rendered result, since that is the only thing this program
// controls.
func TestNotify_TierEmojiAndCompanyTagReplaceTheBriefcaseTag(t *testing.T) {
	server, captured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(server.URL, "job-matches", "", nil)
	n.HTTPClient = server.Client()

	if err := n.Notify(context.Background(), Match{
		Job:   model.Job{Title: "Platform Engineer", Company: "Grafana Labs", Location: "Germany"},
		Score: 0.90,
	}); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	if got := captured.headers.Get("Tags"); got == "briefcase" {
		t.Error("Tags still maps to an emoji, which would render a second emoji beside the tier one")
	}
	if got := captured.headers.Get("Tags"); got != "grafana_labs" {
		t.Errorf("Tags = %q, want %q", got, "grafana_labs")
	}
	if got := captured.headers.Get("Title"); !strings.HasPrefix(got, "⭐ ") {
		t.Errorf("Title = %q, want it to start with the 0.90 tier's emoji", got)
	}
	if got := captured.headers.Get("Priority"); got != "4" {
		t.Errorf("Priority = %q, want %q", got, "4")
	}
}

// TestNotify_KeepsTheLocationSuffixWhenTheTitleIsLong covers the ordering that
// is easy to get backwards: the company/title portion has to be truncated
// against a budget that already accounts for the location, not the assembled
// string truncated afterwards.
//
// Truncating afterwards silently deletes the location - and only for long
// titles, only from the end, and only on the field the suffix exists to
// provide. Nothing else in the pipeline would notice.
func TestNotify_KeepsTheLocationSuffixWhenTheTitleIsLong(t *testing.T) {
	server, captured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(server.URL, "job-matches", "", nil)
	n.HTTPClient = server.Client()

	job := model.Job{
		Title:    strings.Repeat("Senior ", 30) + "Platform Engineer",
		Company:  "Grafana Labs",
		Location: "Germany",
	}
	if err := n.Notify(context.Background(), Match{Job: job, Score: 0.5}); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	title := captured.headers.Get("Title")
	if !strings.HasSuffix(title, " · Germany") {
		t.Errorf("Title = %q, want it to keep the location suffix", title)
	}
	// Strict, not maxTitleChars+3: the budget reserves room for the
	// truncation marker too, so a title that overruns means the reservation
	// arithmetic is wrong rather than merely tight.
	if got := len([]rune(title)); got > maxTitleChars {
		t.Errorf("Title is %d runes, want at most %d", got, maxTitleChars)
	}
}

// TestCompanyTag_SlugifiesNamesThatWouldBreakTheHeader covers the two inputs
// that make slugification a correctness requirement rather than a cosmetic
// one: a comma would split the Tags header into two tags, and a dot would be
// left in place as a literal.
func TestCompanyTag_SlugifiesNamesThatWouldBreakTheHeader(t *testing.T) {
	cases := map[string]string{
		"Grafana Labs":  "grafana_labs",
		"Solo.io":       "solo_io",
		"Solo.io, Inc":  "solo_io_inc",
		"  Spaced  Out": "spaced_out",
		"---":           fallbackTag,
		"":              fallbackTag,
	}
	for in, want := range cases {
		if got := companyTag(in); got != want {
			t.Errorf("companyTag(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestTierFor_BoundariesAreInclusive pins which side of the default 0.85 and
// 0.95 edges a score falls on. The boundaries matter more than they look: the
// live score distribution has 14 matches sitting exactly on 0.85, so an
// off-by-one here flips fourteen notifications at once.
func TestTierFor_BoundariesAreInclusive(t *testing.T) {
	n := NewNtfy("https://ntfy.example.com", "job-matches", "", nil)

	cases := []struct {
		score float64
		emoji string
	}{
		{1.0, "💎"},
		{0.95, "💎"},
		{0.9499, "⭐"},
		{0.85, "⭐"},
		{0.8499, "💼"},
		{0, "💼"},
	}
	for _, tc := range cases {
		if got := n.tierFor(tc.score).Emoji; got != tc.emoji {
			t.Errorf("tierFor(%.4f).Emoji = %q, want %q", tc.score, got, tc.emoji)
		}
	}
}

// TestNotify_OmitsMetadataTheSourceDidNotProvide keeps the metadata line from
// turning into a row of placeholders. The connectors publish different subsets
// of these fields, so a posting from a board without them must simply show a
// shorter line - an "N/A · N/A" would present a missing value as information.
func TestNotify_OmitsMetadataTheSourceDidNotProvide(t *testing.T) {
	server, captured := captureServer(t, http.StatusOK, "")
	n := NewNtfy(server.URL, "job-matches", "", nil)
	n.HTTPClient = server.Client()

	bare := model.Job{Title: "Platform Engineer", Company: "Acme"}
	if err := n.Notify(context.Background(), Match{Job: bare, Reason: "because"}); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}
	if captured.body != "because" {
		t.Errorf("body = %q, want just the reason when no metadata is available", captured.body)
	}

	rich := bare
	rich.Department = "R&D: Platform"
	rich.WorkplaceType = "Remote"
	rich.EmploymentType = "Full-time"
	rich.PostedAt = time.Date(2026, 3, 1, 6, 0, 0, 0, time.UTC)
	n.Now = func() time.Time { return time.Date(2026, 3, 12, 6, 0, 0, 0, time.UTC) }

	if err := n.Notify(context.Background(), Match{Job: rich, Score: 0.9, Reason: "because", Signals: []string{"Crossplane", "Terraform"}}); err != nil {
		t.Fatalf("Notify returned error: %v", err)
	}

	want := "R&D: Platform · Remote · Full-time · Posted 11 days ago\nMatched: Crossplane, Terraform\n\nbecause"
	if captured.body != want {
		t.Errorf("body =\n%q\nwant\n%q", captured.body, want)
	}
}
