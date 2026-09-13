package filter

import (
	"strings"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

// Location decision kinds, reported in LocationDecision.Kind.
const (
	// LocationKindAccept means an accept entry matched: the posting names a
	// place the operator can work from.
	LocationKindAccept = "accept"
	// LocationKindAmbiguous means the location named no accepted place but
	// said nothing about a country either - it was empty, it signalled
	// remote, or it was a filler value like "N/A". Only the AI scorer can
	// judge those.
	LocationKindAmbiguous = "ambiguous"
	// LocationKindUnmatched means the location named a place that is not
	// accepted, so the configured unmatched mode decided it.
	LocationKindUnmatched = "unmatched"
	// LocationKindDisabled means the accept list was empty, so the location
	// pre-filter is off and every location is acceptable.
	LocationKindDisabled = "disabled"
)

// ambiguousMarkers are the phrasings that say "this posting does not pin the
// candidate to one country". They exist so that a genuinely location-free
// posting reaches the AI scorer instead of being guessed at - and dropped -
// by the pre-filter. Enumerating them in config is exactly what failed
// before, when a bare "Remote" or "Anywhere" matched no entry and, with
// unmatched at its default, nothing at all.
//
// Two groups, both ambiguous for the same reason: the pre-filter cannot say
// where the posting legally is, and the AI scorer is the part of the pipeline
// that can (it carries the relocation rule in ai.profile).
//
// The list is not operator-configurable on purpose. A phrasing that should be
// accepted outright belongs in Accept, which has the same effect with no new
// config surface.
var ambiguousMarkers = []string{
	// Remote signalling.
	"remote",
	"worldwide",
	"world wide",
	"anywhere",
	"global",
	"distributed",
	"telecommute",
	"telework",
	"wfh",
	"work from home",
	"work from anywhere",
	"home office",
	"homeoffice",
	"ortsunabhängig",
	"virtual",
	"location independent",

	// Devoid of geography: the field was filled in, but it names no place.
	// "N/A" (which normalizes to "n a") is the common one, and without it a
	// portal that cannot express a location would have its whole inventory
	// dropped for free.
	"n a",
	"not specified",
	"unspecified",
	"unknown",
	"flexible",
	"various",
	"multiple locations",
	"any location",
}

// LocationDecision is the outcome of the location pre-filter, carrying
// enough detail to explain a rejection in the logs. Without that detail, a
// posting dropped for its location is indistinguishable from one dropped
// for its keywords.
type LocationDecision struct {
	// Passed reports whether the posting may proceed to AI scoring.
	Passed bool

	// Rule is the accept entry or remote marker that decided the outcome, or
	// "" when the outcome did not come from a specific term (an unmatched or
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
// from matching "Australia" or "Belarus", and what let an accept entry of
// "Germany" match "Remote - Germany" - the match is on the place, so the way
// a posting decorates it with "Remote" or a region does not matter.
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

// ambiguousMarker returns the marker that makes a location ambiguous, or ""
// and false when the location carries no such signal.
func ambiguousMarker(normalized string) (string, bool) {
	for _, marker := range ambiguousMarkers {
		if matchesLocation(normalized, marker) {
			return marker, true
		}
	}
	return "", false
}

// MatchLocation applies cfg to job's location and reports the outcome along
// with the term that decided it.
//
// The accept list is a whitelist of places the operator can legally work
// from, and it is the only thing a location has to match to be accepted.
// Precedence, in order:
//
//  1. An accept entry matches: accepted. A posting located "Remote - Germany"
//     is accepted here, because it names Germany.
//  2. No accept entries at all: accepted, because the pre-filter is disabled.
//  3. The location says nothing about a country - it is empty, it signals
//     remote, or it is uninformative ("N/A") - so it is accepted, and handed
//     to the AI scorer. This is the fixed path: the pre-filter cannot say
//     which country such a role is legally tied to, and enumerating every
//     phrasing a portal might use for "anywhere" is not solvable, so it
//     never guesses and never drops.
//  4. Otherwise the location names a place that is not accepted: rejected, or
//     passed through to the AI scorer when Unmatched is
//     config.LocationUnmatchedPass.
//
// There is no deny list, and no special handling of "remote" as a place. A
// posting located "Remote - Canada" matches no accept entry, so it reaches
// the scorer as ambiguous (the actor that can apply the relocation rule),
// while "Toronto, Canada" is dropped for free.
func MatchLocation(job model.Job, cfg config.LocationConfig) LocationDecision {
	haystack := normalizeLocation(job.Location)

	// The empty accept list is checked first because it means "do not filter
	// on location at all": nothing could match, but nothing should be
	// rejected either.
	if len(cfg.Accept) == 0 {
		return LocationDecision{Passed: true, Kind: LocationKindDisabled}
	}

	if haystack != "" {
		for _, rule := range cfg.Accept {
			if matchesLocation(haystack, rule) {
				return LocationDecision{Passed: true, Rule: rule, Kind: LocationKindAccept}
			}
		}
	}

	if haystack == "" {
		return LocationDecision{Passed: true, Kind: LocationKindAmbiguous}
	}
	if marker, ok := ambiguousMarker(haystack); ok {
		return LocationDecision{Passed: true, Rule: marker, Kind: LocationKindAmbiguous}
	}

	return LocationDecision{Passed: cfg.Unmatched == config.LocationUnmatchedPass, Kind: LocationKindUnmatched}
}
