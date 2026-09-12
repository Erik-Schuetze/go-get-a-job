package sanitize

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSingleLine(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		maxLen int
		want   string
	}{
		{"plain text is unchanged", "Platform Engineer", 0, "Platform Engineer"},
		{"newlines collapse to a space", "Acme\nInc", 0, "Acme Inc"},
		{"tabs collapse to a space", "Acme\tInc", 0, "Acme Inc"},
		{"runs of whitespace collapse", "Acme   \n\t  Inc", 0, "Acme Inc"},
		{"leading and trailing whitespace is trimmed", "  Acme  ", 0, "Acme"},
		{"CRLF is treated as one line break", "Acme\r\nInc", 0, "Acme Inc"},
		{"a bare CR is a line break", "Acme\rInc", 0, "Acme Inc"},
		{"ANSI escape sequences are removed", "\x1b[31mRed\x1b[0m", 0, "[31mRed[0m"},
		{"an OSC sequence is removed", "\x1b]0;title\x07text", 0, "]0;titletext"},
		{"null bytes are dropped", "Ac\x00me", 0, "Acme"},
		{"DEL is dropped", "Ac\x7fme", 0, "Acme"},
		{"C1 controls are dropped", "Ac\u009fme", 0, "Acme"},
		{"a C1 line separator becomes a space", "Ac\u0085me", 0, "Ac me"},
		{"zero-width characters are dropped", "Ac\u200bme", 0, "Acme"},
		{"a right-to-left override is dropped", "Ac\u202eme", 0, "Acme"},
		{"a byte-order mark is dropped", "\ufeffAcme", 0, "Acme"},
		{"empty input stays empty", "", 0, ""},
		{"input of only controls becomes empty", "\n\t\x00", 0, ""},
		{"a cap is applied with an ellipsis marker", "abcdefghij", 4, "abcd..."},
		{"text at the cap is untouched", "abcd", 4, "abcd"},
		{"a non-positive cap means no cap", "abcdefghij", -1, "abcdefghij"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SingleLine(tt.in, tt.maxLen); got != tt.want {
				t.Errorf("SingleLine(%q, %d) = %q, want %q", tt.in, tt.maxLen, got, tt.want)
			}
		})
	}
}

// A header value built from untrusted text must never contain a newline:
// Go's http client rejects the whole request if it does, which would let one
// hostile posting silence the notification for every job that matched with
// it.
func TestSingleLine_NeverEmitsHeaderBreakingBytes(t *testing.T) {
	hostile := "Acme\r\nX-Injected: 1\nInc\x00\x1b[2J\u202e"

	got := SingleLine(hostile, 0)
	if strings.ContainsAny(got, "\r\n\x00") {
		t.Fatalf("SingleLine left header-breaking bytes in %q", got)
	}
	if !utf8.ValidString(got) {
		t.Fatalf("SingleLine returned invalid UTF-8: %q", got)
	}
}

func TestMultiLine(t *testing.T) {
	tests := []struct {
		name   string
		in     string
		maxLen int
		want   string
	}{
		{"newlines are preserved", "line one\nline two", 0, "line one\nline two"},
		{"tabs are preserved", "a\tb", 0, "a\tb"},
		{"CRLF normalizes to LF", "a\r\nb", 0, "a\nb"},
		{"a bare CR normalizes to LF", "a\rb", 0, "a\nb"},
		{"ANSI escapes are still removed", "\x1b[31mRed\x1b[0m", 0, "[31mRed[0m"},
		{"a long run of blank lines collapses to one paragraph break", "a\n\n\n\n\n\nb", 0, "a\n\nb"},
		{"leading and trailing whitespace is trimmed", "\n\n a \n\n", 0, "a"},
		{"a cap is applied with an ellipsis suffix", strings.Repeat("a", 20), 5, "aaaaa..."},
		{"empty input stays empty", "", 0, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MultiLine(tt.in, tt.maxLen); got != tt.want {
				t.Errorf("MultiLine(%q, %d) = %q, want %q", tt.in, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{"shorter than the cap is returned as-is", "abc", 10, "abc"},
		{"exactly the cap is returned as-is", "abcde", 5, "abcde"},
		{"longer than the cap gains a suffix", "abcdefg", 5, "abcde..."},
		{"a zero cap means no cap", "abcdefg", 0, "abcdefg"},
		{"a negative cap means no cap", "abcdefg", -3, "abcdefg"},
		{
			// The bug this function exists to fix: slicing by byte would split
			// the final "é" (2 bytes) and produce invalid UTF-8 in the payload.
			name: "a multi-byte rune on the boundary is not split",
			in:   "café",
			max:  3,
			want: "caf...",
		},
		{
			name: "an emoji on the boundary is not split",
			in:   "ab😀cd",
			max:  3,
			want: "ab😀...",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TruncateRunes(tt.in, tt.max)
			if got != tt.want {
				t.Errorf("TruncateRunes(%q, %d) = %q, want %q", tt.in, tt.max, got, tt.want)
			}
			if !utf8.ValidString(got) {
				t.Errorf("TruncateRunes(%q, %d) returned invalid UTF-8: %q", tt.in, tt.max, got)
			}
		})
	}
}

func TestLink(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"https is accepted", "https://example.com/job/1", "https://example.com/job/1"},
		{"http is accepted", "http://example.com/job/1", "http://example.com/job/1"},
		{"query and fragment are preserved", "https://example.com/j?a=1#b", "https://example.com/j?a=1#b"},
		{"surrounding whitespace is trimmed", "  https://example.com  ", "https://example.com"},
		{"javascript is rejected", "javascript:alert(1)", ""},
		{"a javascript URL with mixed case is rejected", "JaVaScRiPt:alert(1)", ""},
		{"data is rejected", "data:text/html,<script>alert(1)</script>", ""},
		{"file is rejected", "file:///etc/passwd", ""},
		{"an intent URL is rejected", "intent://scan/#Intent;scheme=zxing;end", ""},
		{"a scheme-relative URL is rejected", "//example.com/job", ""},
		{"a relative path is rejected", "/job/1", ""},
		{"a bare hostname with no scheme is rejected", "example.com/job", ""},
		{"an empty string is rejected", "", ""},
		{"an empty http URL is rejected", "https://", ""},
		{"a control character is rejected", "https://example.com/\nX", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Link(tt.in); got != tt.want {
				t.Errorf("Link(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// A very long run of control characters must not be amplified into a much
// larger buffer as it is collapsed.
func TestSingleLine_DoesNotAmplifyShortInputs(t *testing.T) {
	in := strings.Repeat("a\x00", 1000)
	got := SingleLine(in, 10)
	if !strings.HasPrefix(got, strings.Repeat("a", 10)) {
		t.Errorf("expected the 1000 'a's to survive the collapsed NULs and then truncate, got %q", got)
	}
	if len(got) > 13 {
		t.Errorf("expected the result to be capped, got %d bytes", len(got))
	}
}
