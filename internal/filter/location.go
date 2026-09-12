package filter

import (
	"strings"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

// Location decision kinds, reported in LocationDecision.Kind.
const (
	// LocationKindDeny means a deny entry matched. Deny always wins.
	LocationKindDeny = "deny"
	// LocationKindAllow means an allow entry matched.
	LocationKindAllow = "allow"
	// LocationKindUnmatched means neither list matched, so the configured
	// unmatched mode decided it.
	LocationKindUnmatched = "unmatched"
	// LocationKindDisabled means the allow list was empty, so every
	// location is acceptable.
	LocationKindDisabled = "disabled"
)

// LocationDecision is the outcome of the location pre-filter, carrying
// enough detail to explain a rejection in the logs. Without that detail, a
// posting dropped for its location is indistinguishable from one dropped
// for its keywords.
type LocationDecision struct {
	// Passed reports whether the posting may proceed to AI scoring.
	Passed bool

	// Rule is the allow or deny entry that decided the outcome, or "" when
	// the outcome did not come from a specific entry (an unmatched or
	// disabled decision).
	Rule string

	// Kind is one of the LocationKind constants: which half of the check
	// decided the outcome.
	Kind string
}

// normalizeLocation lowercases s and reduces every run of characters that
// are not ASCII letters or digits to a single space, then trims. It is what
// makes "Remote - Canada", "remote,canada", and "REMOTE / CANADA" all
// compare equal.
func normalizeLocation(s string) string {
	var b strings.Builder
	b.Grow(len(s))

	pendingSpace := false
	for _, r := range strings.ToLower(s) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pendingSpace && b.Len() > 0 {
				b.WriteByte(' ')
			}
			pendingSpace = false
			b.WriteRune(r)
			continue
		}
		// Anything else (punctuation, separators, non-ASCII) becomes a
		// separator. Deferring the write collapses runs and drops leading
		// separators.
		pendingSpace = true
	}

	return b.String()
}

// matchesLocation reports whether pattern appears in haystack as a whole
// sequence of words. Both sides are normalized first.
//
// Word boundaries, rather than a plain substring test, are what stop "US"
// from matching "Australia" or "Belarus" and what let "Remote" match
// "Remote - Canada" only because the caller chose to allow that phrasing -
// the match is on the word, and geographic scope is the caller's decision.
func matchesLocation(haystack, pattern string) bool {
	pattern = normalizeLocation(pattern)
	if pattern == "" {
		return false
	}

	haystack = normalizeLocation(haystack)
	if haystack == "" {
		return false
	}

	// Padding both sides with a space turns the boundary checks into plain
	// substring lookups: " us " appears in " austin us " but not in
	// " australia ".
	return strings.Contains(" "+haystack+" ", " "+pattern+" ")
}

// MatchLocation applies cfg to job's location and reports the outcome along
// with the entry that decided it.
//
// Precedence, in order:
//
//  1. A deny entry matches: rejected. Deny wins even when an allow entry
//     also matches, which is what keeps "Remote - Canada" from riding in on
//     an allow entry of "Remote".
//  2. An allow entry matches: accepted.
//  3. No allow entries at all: accepted (deny was already applied above).
//  4. Otherwise unmatched: rejected, or passed through to the AI scorer
//     when Unmatched is config.LocationUnmatchedPass.
//
// Remote postings get no special treatment anywhere here. A posting located
// "Remote - Canada" is judged exactly like "Toronto, Canada", because the
// constraint it represents is the same one. A posting that is genuinely
// location-free is handled by listing the phrasing you accept in Allow
// ("Remote (Global)", "Worldwide") or by setting Unmatched to pass and
// letting the AI scorer judge it against the profile.
func MatchLocation(job model.Job, cfg config.LocationConfig) LocationDecision {
	// An empty location string cannot match anything, so it is unmatched
	// rather than silently rejected by the deny list.
	haystack := normalizeLocation(job.Location)

	if haystack != "" {
		for _, rule := range cfg.Deny {
			if matchesLocation(haystack, rule) {
				return LocationDecision{Passed: false, Rule: rule, Kind: LocationKindDeny}
			}
		}

		for _, rule := range cfg.Allow {
			if matchesLocation(haystack, rule) {
				return LocationDecision{Passed: true, Rule: rule, Kind: LocationKindAllow}
			}
		}
	}

	if len(cfg.Allow) == 0 {
		return LocationDecision{Passed: true, Kind: LocationKindDisabled}
	}

	return LocationDecision{Passed: cfg.Unmatched == config.LocationUnmatchedPass, Kind: LocationKindUnmatched}
}
