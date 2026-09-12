// Package sanitize neutralizes text that came from a third party before it
// is written to a log line, an HTTP header, or an error message.
//
// Job postings are attacker-controlled: anyone can publish one, on any of
// the boards this tool polls. Almost every field a connector reads out of a
// posting (title, company, location, description) is therefore untrusted,
// as is anything an AI provider echoes back about it. Nothing in this
// package fixes a vulnerability found in this codebase - it is a boundary
// that keeps untrusted bytes from acquiring meaning they were not meant to
// have once they leave the pipeline:
//
//   - Control characters (including ANSI/CSI escape sequences) would let a
//     crafted title rewrite what a log line appears to say, or move a
//     terminal cursor while an operator is reading it.
//   - Newlines and other invalid bytes in an HTTP header value make Go's
//     http client refuse to send the request at all, so one hostile posting
//     could otherwise silence the notification for every job that matched
//     alongside it.
//   - A non-http(s) URL in a notification's tap target turns a matched job
//     into a way to hand the operator's phone an arbitrary URI scheme.
//
// Every function is safe to call on trusted text too, so callers never need
// to decide which is which.
package sanitize

import (
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SingleLine makes s safe to use as a single-line value such as an HTTP
// header field or a log attribute. It:
//
//   - turns any kind of whitespace into a single space, so a value that
//     spans lines or columns cannot inject an extra header or log record;
//   - removes every other control and invisible character outright, rather
//     than substituting for it - a zero-width space or an RTL override is
//     not a word separator, and replacing one with a space would change
//     what the text says ("Acme" must not become "Ac me");
//   - caps the result at maxLen runes, appending an ellipsis when it had to
//     cut. A maxLen of 0 or less means no cap.
//
// The result is guaranteed to be valid UTF-8, even if s was not, and never
// contains an unprintable character - so it is also safe to write to a
// terminal.
func SingleLine(s string, maxLen int) string {
	var b strings.Builder
	b.Grow(len(s))

	// Collapses whitespace runs without a regexp, and caps the input as it
	// goes, so a long string of control characters can't be amplified into
	// a much larger buffer.
	pendingSpace := false
	for _, r := range s {
		// Whitespace is checked first: the newline and tab this is meant to
		// collapse are themselves control characters, so testing isControl
		// first would delete the word break instead of applying it.
		if unicode.IsSpace(r) {
			if b.Len() > 0 {
				pendingSpace = true
			}
			continue
		}
		if isControl(r) {
			continue
		}
		if pendingSpace {
			b.WriteByte(' ')
			pendingSpace = false
		}
		b.WriteRune(r)
	}

	return TruncateRunes(strings.TrimSpace(b.String()), maxLen)
}

// MultiLine is SingleLine's counterpart for values that are legitimately
// allowed to span lines, such as a notification body or an AI-written
// explanation: it keeps newlines and tabs but still removes every other
// control and invisible character, and caps the length. Runs of blank lines
// collapse to a single paragraph break so a body made mostly of newlines
// can't push the real content out of view.
func MultiLine(s string, maxLen int) string {
	var b strings.Builder
	b.Grow(len(s))

	// Normalizes CRLF and CR to LF first, then treats a run of newlines as
	// a single paragraph break.
	s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	blankLines := 0
	for _, r := range s {
		switch {
		case r == '\n':
			blankLines++
			if blankLines > 2 {
				continue
			}
			b.WriteRune(r)
		case isControl(r) && r != '\t':
			continue
		default:
			blankLines = 0
			b.WriteRune(r)
		}
	}

	return TruncateRunes(strings.TrimSpace(b.String()), maxLen)
}

// TruncateRunes caps s at max runes, appending an ellipsis-style suffix when
// it had to cut. It counts runes rather than bytes so a multi-byte UTF-8
// character straddling the boundary is never split into invalid UTF-8
// (which would produce a request body or log line no decoder can read).
//
// A max of 0 or less means "no cap".
func TruncateRunes(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return string([]rune(s)[:max]) + "..."
}

// Link returns raw if it is a well-formed absolute http or https URL with a
// host, and "" otherwise.
//
// It exists for values that become a tap target - a notification's Click
// header, for instance. Those come straight out of a third-party posting,
// so without this check a job could hand the operator's phone a
// javascript:, data:, or intent:// URI instead of a link to the posting.
// Callers should treat an empty return as "no link", not as an error.
func Link(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	if u.Host == "" {
		return ""
	}
	return u.String()
}

// isControl reports whether r is a C0 or C1 control character, or one of
// the handful of specials that would let a caller reach past whatever is
// rendering the text (e.g. RTL overrides, which can visually reorder a line,
// or the zero-width characters that can hide text entirely).
func isControl(r rune) bool {
	switch {
	case r < 0x20: // C0 controls
		return true
	case r == 0x7f: // DEL
		return true
	case r >= 0x80 && r <= 0x9f: // C1 controls
		return true
	// Bidirectional overrides and isolates, and the zero-width set: these
	// are not "control characters" by Unicode category, but they let a
	// string render as something other than what it contains.
	case r == 0x200b, r == 0x200c, r == 0x200d, r == 0x200e, r == 0x200f:
		return true
	case r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	case r == 0xfeff:
		return true
	default:
		return false
	}
}
