package sources

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildURL(t *testing.T) {
	const base = "https://boards-api.greenhouse.io/v1/boards"

	tests := []struct {
		name     string
		base     string
		rawQuery string
		segments []string
		want     string
	}{
		{
			name:     "path segments are joined in order",
			base:     base,
			segments: []string{"grafanalabs", "jobs"},
			want:     "https://boards-api.greenhouse.io/v1/boards/grafanalabs/jobs",
		},
		{
			name:     "a trailing slash on the base is not doubled",
			base:     base + "/",
			segments: []string{"grafanalabs"},
			want:     "https://boards-api.greenhouse.io/v1/boards/grafanalabs",
		},
		{
			name:     "a raw query is appended verbatim",
			base:     base,
			rawQuery: "content=true",
			segments: []string{"grafanalabs"},
			want:     "https://boards-api.greenhouse.io/v1/boards/grafanalabs?content=true",
		},
		{
			name:     "a space in a segment is escaped, not treated as a break",
			base:     base,
			segments: []string{"acme inc"},
			want:     "https://boards-api.greenhouse.io/v1/boards/acme%20inc",
		},
		{
			name:     "a slash in a segment cannot add a path segment",
			base:     base,
			segments: []string{"acme/../admin"},
			want:     "https://boards-api.greenhouse.io/v1/boards/acme%2F..%2Fadmin",
		},
		{
			name:     "a question mark in a segment cannot replace the query",
			base:     base,
			rawQuery: "content=true",
			segments: []string{"acme?x=1"},
			want:     "https://boards-api.greenhouse.io/v1/boards/acme%3Fx=1?content=true",
		},
		{
			name:     "a fragment character in a segment is escaped",
			base:     base,
			segments: []string{"acme#frag"},
			want:     "https://boards-api.greenhouse.io/v1/boards/acme%23frag",
		},
		{
			name:     "a newline in a segment cannot inject a header",
			base:     base,
			segments: []string{"acme\nHost: evil"},
			want:     "https://boards-api.greenhouse.io/v1/boards/acme%0AHost:%20evil",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := buildURL(tt.base, tt.rawQuery, tt.segments...)
			if err != nil {
				t.Fatalf("buildURL returned unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("buildURL =\n  %q\nwant\n  %q", got, tt.want)
			}
		})
	}
}

func TestBuildURL_RejectsBadBases(t *testing.T) {
	tests := []struct {
		name string
		base string
	}{
		{"a relative base", "/v1/boards"},
		{"a scheme-less base", "boards-api.greenhouse.io/v1"},
		{"a scheme-only base", "https://"},
		{"a base with credentials", "https://user:pass@boards-api.greenhouse.io"},
		{"a base that already has a query", "https://boards-api.greenhouse.io/v1?x=1"},
		{"a base that already has a fragment", "https://boards-api.greenhouse.io/v1#x"},
		{"an unparseable base", "https://%zz/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := buildURL(tt.base, "", "segment"); err == nil {
				t.Fatalf("expected base %q to be rejected", tt.base)
			}
		})
	}
}

func TestBuildURL_RejectsEmptySegment(t *testing.T) {
	if _, err := buildURL("https://example.com", "", "a", "", "b"); err == nil {
		t.Fatal("expected an empty segment to be rejected rather than silently collapsing a path separator")
	}
}

func TestSanitizePathSuffix(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"a normal path is kept", "/job/Platform-Engineer_R1234", "/job/Platform-Engineer_R1234"},
		{"a path without a leading slash gains one", "job/123", "/job/123"},
		{"an empty string yields nothing", "", ""},
		{"whitespace yields nothing", "   ", ""},
		{"a root path yields nothing", "/", ""},
		{
			name: "an absolute URL has its scheme and host dropped",
			in:   "https://evil.example.com/job/1",
			want: "/job/1",
		},
		{
			name: "a protocol-relative value has its host dropped",
			in:   "//evil.example.com/job/1",
			want: "/job/1",
		},
		{
			name: "a query is truncated",
			in:   "/job/1?redirect=https://evil.example.com",
			want: "/job/1",
		},
		{
			name: "a fragment is truncated",
			in:   "/job/1#frag",
			want: "/job/1",
		},
		{
			name: "traversal cannot climb above the prefix",
			in:   "../../../../etc/passwd",
			want: "/etc/passwd",
		},
		{
			name: "traversal in the middle is resolved",
			in:   "/job/../../admin",
			want: "/admin",
		},
		{
			// Percent-encoding does not smuggle a traversal past the
			// cleaning step: the value is decoded first, then resolved
			// against "/". What the caller is guaranteed is a rooted path,
			// not an unchanged one.
			name: "an already-encoded traversal is resolved, not passed through",
			in:   "/job/%2e%2e%2fadmin",
			want: "/admin",
		},
		{
			name: "a space is escaped",
			in:   "/job/my role",
			want: "/job/my%20role",
		},
		{
			name: "a newline is escaped",
			in:   "/job/1\nX",
			want: "/job/1%0AX",
		},
		{
			name: "surrounding whitespace is trimmed",
			in:   "  /job/1  ",
			want: "/job/1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizePathSuffix(tt.in); got != tt.want {
				t.Errorf("sanitizePathSuffix(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// An untrusted value can change which path is requested on the fixed origin,
// but it must never be able to change the origin itself - that would turn a
// job posting into request redirection to a host of the poster's choosing.
func TestSanitizePathSuffix_CannotChangeOrigin(t *testing.T) {
	hostile := []string{
		"https://evil.example.com/steal",
		"//evil.example.com/steal",
		"http://evil.example.com/steal",
		"/job/1?next=https://evil.example.com",
	}

	for _, raw := range hostile {
		suffix := sanitizePathSuffix(raw)
		if strings.Contains(suffix, "evil.example.com") && strings.HasPrefix(suffix, "//") {
			t.Errorf("sanitizePathSuffix(%q) = %q, which could be read as a new authority", raw, suffix)
		}
		if strings.Contains(suffix, "://") {
			t.Errorf("sanitizePathSuffix(%q) = %q, which still contains a scheme", raw, suffix)
		}
		if !strings.HasPrefix(suffix, "/") {
			t.Errorf("sanitizePathSuffix(%q) = %q, expected a rooted path", raw, suffix)
		}
	}
}

// End-to-end: a hostile board token must not be able to move the Greenhouse
// request off the fixed path prefix.
func TestGreenhouse_HostileCompanySlugStaysOnPath(t *testing.T) {
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jobs":[]}`))
	}))
	defer server.Close()

	g := &Greenhouse{
		Company:    "acme/../../admin",
		BaseURL:    server.URL,
		HTTPClient: server.Client(),
	}

	// The request itself will fail (the handler returns a shape the connector
	// may not like), but the point is the path it asked for.
	if _, err := g.Fetch(t.Context()); err != nil {
		t.Logf("fetch returned an error (acceptable for this assertion): %v", err)
	}

	if strings.Contains(gotPath, "../") {
		t.Fatalf("the board token escaped the path prefix: %q", gotPath)
	}
	if !strings.Contains(gotPath, "%2F") {
		t.Fatalf("expected the token's slashes to be percent-encoded, got %q", gotPath)
	}
}
