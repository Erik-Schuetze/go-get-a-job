package filter

import (
	"strings"
	"testing"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

func TestNormalizeLocation(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"lowercases", "BERLIN", "berlin"},
		{"punctuation becomes a separator", "Remote - Canada", "remote canada"},
		{"separator runs collapse", "remote;   canada", "remote canada"},
		{"leading and trailing separators drop", "  (Remote) ", "remote"},
		{"digits survive", "EMEA-2", "emea 2"},
		{"non-ascii becomes a separator", "München, DE", "m nchen de"},
		{"empty stays empty", "   ", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeLocation(tt.in); got != tt.want {
				t.Errorf("normalizeLocation(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestMatchesLocation(t *testing.T) {
	tests := []struct {
		name     string
		haystack string
		pattern  string
		want     bool
	}{
		{"exact word", "berlin germany", "germany", true},
		{"word inside a longer location", "berlin, germany (remote)", "germany", true},
		// The boundary rule is the whole point: a two-letter country code
		// must not match a country whose name merely contains it.
		{"us does not match australia", "australia", "us", false},
		{"us does not match belarus", "belarus", "us", false},
		{"us does not match prussia", "prussia", "us", false},
		{"us matches a standalone us", "austin, us", "us", true},
		{"multi-word phrase", "austin, united states", "united states", true},
		{"multi-word phrase across separators", "austin, united-states", "united states", true},
		{"phrase in the wrong order does not match", "states united", "united states", false},
		{"empty pattern never matches", "berlin", "", false},
		{"empty haystack never matches", "", "berlin", false},
		{"punctuation-only pattern never matches", "berlin", "---", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := matchesLocation(tt.haystack, tt.pattern); got != tt.want {
				t.Errorf("matchesLocation(%q, %q) = %v, want %v", tt.haystack, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestMatchLocation(t *testing.T) {
	// The personal-config shape this filter has to serve: based in
	// Germany, not willing to relocate, so anywhere legally unworkable is
	// denied outright.
	personal := config.LocationConfig{
		Allow:     []string{"Germany", "EMEA", "European Union", "Remote (Global)", "Worldwide"},
		Deny:      []string{"Canada", "United States", "US", "India"},
		Unmatched: config.LocationUnmatchedReject,
	}

	tests := []struct {
		name       string
		location   string
		cfg        config.LocationConfig
		wantPassed bool
		wantKind   string
		wantRule   string
	}{
		{
			// The regression test for the reported leak. Under the old
			// substring allow-list this passed on "Remote" alone, handed a
			// Canadian posting to the AI, and got it notified.
			name:       "remote canada is denied",
			location:   "Remote - Canada",
			cfg:        personal,
			wantPassed: false,
			wantKind:   LocationKindDeny,
			wantRule:   "Canada",
		},
		{
			// Deny wins over a matching allow entry, which is the property
			// that makes a broad allow entry like "Remote" safe to write.
			name:       "deny beats allow",
			location:   "Remote - Canada",
			cfg:        config.LocationConfig{Allow: []string{"Remote"}, Deny: []string{"Canada"}},
			wantPassed: false,
			wantKind:   LocationKindDeny,
			wantRule:   "Canada",
		},
		{
			name:       "deny beats allow even when the allow entry matches first",
			location:   "Berlin, Germany (Remote)",
			cfg:        config.LocationConfig{Allow: []string{"Germany"}, Deny: []string{"Germany"}},
			wantPassed: false,
			wantKind:   LocationKindDeny,
			wantRule:   "Germany",
		},
		{
			name:       "onsite germany allowed",
			location:   "Berlin, Germany",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAllow,
			wantRule:   "Germany",
		},
		{
			name:       "germany with remote suffix allowed",
			location:   "Berlin, Germany (Remote)",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAllow,
			wantRule:   "Germany",
		},
		{
			name:       "usa denied",
			location:   "Austin, US",
			cfg:        personal,
			wantPassed: false,
			wantKind:   LocationKindDeny,
			wantRule:   "US",
		},
		{
			// The other half of the boundary rule: "Australia" contains
			// "us" but must not be treated as the United States. It is
			// unmatched here, so the configured default decides.
			name:       "australia is unmatched, not denied",
			location:   "Sydney, Australia",
			cfg:        personal,
			wantPassed: false,
			wantKind:   LocationKindUnmatched,
			wantRule:   "",
		},
		{
			name:       "worldwide remote allowed",
			location:   "Remote (Global)",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAllow,
			wantRule:   "Remote (Global)",
		},
		{
			name:       "worldwide allowed",
			location:   "Worldwide",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAllow,
			wantRule:   "Worldwide",
		},
		{
			// A bare "Remote" says nothing about legal location, which is
			// exactly the ambiguity that should reach the AI scorer rather
			// than be guessed at here.
			name:       "bare remote is unmatched and rejected by default",
			location:   "Remote",
			cfg:        personal,
			wantPassed: false,
			wantKind:   LocationKindUnmatched,
		},
		{
			name:       "bare remote is unmatched and passed when configured",
			location:   "Remote",
			cfg:        config.LocationConfig{Allow: []string{"Germany"}, Unmatched: config.LocationUnmatchedPass},
			wantPassed: true,
			wantKind:   LocationKindUnmatched,
		},
		{
			name:       "bare remote passes when allow is empty and deny cannot match",
			location:   "Remote",
			cfg:        config.LocationConfig{Deny: []string{"Canada"}},
			wantPassed: true,
			wantKind:   LocationKindDisabled,
		},
		{
			name:       "deny-only config still denies",
			location:   "Toronto, Canada",
			cfg:        config.LocationConfig{Deny: []string{"Canada"}},
			wantPassed: false,
			wantKind:   LocationKindDeny,
			wantRule:   "Canada",
		},
		{
			name:       "both lists empty passes everything",
			location:   "Toronto, Canada",
			cfg:        config.LocationConfig{},
			wantPassed: true,
			wantKind:   LocationKindDisabled,
		},
		{
			name:       "empty location is unmatched, not denied",
			location:   "",
			cfg:        personal,
			wantPassed: false,
			wantKind:   LocationKindUnmatched,
		},
		{
			name:       "punctuation-separated canada is still denied",
			location:   "remote;canada",
			cfg:        personal,
			wantPassed: false,
			wantKind:   LocationKindDeny,
			wantRule:   "Canada",
		},
		{
			name:       "mixed-case and dashed compound",
			location:   "EU-EMEA",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAllow,
			wantRule:   "EMEA",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := MatchLocation(model.Job{Location: tt.location}, tt.cfg)
			if got.Passed != tt.wantPassed {
				t.Errorf("MatchLocation(%q).Passed = %v, want %v", tt.location, got.Passed, tt.wantPassed)
			}
			if got.Kind != tt.wantKind {
				t.Errorf("MatchLocation(%q).Kind = %q, want %q", tt.location, got.Kind, tt.wantKind)
			}
			if tt.wantRule != "" && got.Rule != tt.wantRule {
				t.Errorf("MatchLocation(%q).Rule = %q, want %q", tt.location, got.Rule, tt.wantRule)
			}
		})
	}
}

func TestMatchLocation_CaseInsensitiveListEntries(t *testing.T) {
	// Operators write lists in whatever case feels natural; the matching
	// must not care.
	cfg := config.LocationConfig{
		Allow: []string{"  GERMANY  ", "worldwide"},
		Deny:  []string{"CANADA"},
	}

	if got := MatchLocation(model.Job{Location: "berlin, germany"}, cfg); !got.Passed {
		t.Errorf("expected untrimmed, uppercase allow entries to match, got %+v", got)
	}
	if got := MatchLocation(model.Job{Location: "TORONTO, CANADA"}, cfg); got.Passed {
		t.Errorf("expected uppercase deny entries to match, got %+v", got)
	}
}

func TestMatchLocation_LongLocationString(t *testing.T) {
	// Postings in the wild carry long location strings ("Berlin, Germany;
	// London, United Kingdom; Remote - Canada"). The deny list must still
	// find its entry anywhere in the string.
	cfg := config.LocationConfig{
		Allow: []string{"Germany"},
		Deny:  []string{"Canada"},
	}
	location := "Berlin, Germany; London, United Kingdom; Remote - Canada"

	if got := MatchLocation(model.Job{Location: location}, cfg); got.Passed {
		t.Errorf("expected multi-region posting listing Canada to be denied, got %+v", got)
	}

	// Same shape, without the denied region.
	location = strings.Repeat("Berlin, Germany; ", 20)
	if got := MatchLocation(model.Job{Location: location}, cfg); !got.Passed {
		t.Errorf("expected Germany-only posting to pass, got %+v", got)
	}
}
