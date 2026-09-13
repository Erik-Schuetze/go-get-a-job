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
	// The shape this filter has to serve: based in Germany, not willing to
	// relocate, so only places that are legally workable are named here.
	personal := config.LocationConfig{
		Accept:    []string{"Germany", "EMEA", "European Union"},
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
			name:       "onsite germany accepted",
			location:   "Berlin, Germany",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAccept,
			wantRule:   "Germany",
		},
		{
			// The old allow-list needed a "Remote (Global)" entry to catch
			// this, and a portal writing "Remote - Germany" matched nothing.
			// Naming the country is enough now; the decorators do not matter.
			name:       "germany with remote suffix accepted",
			location:   "Berlin, Germany (Remote)",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAccept,
			wantRule:   "Germany",
		},
		{
			name:       "remote prefixed germany accepted",
			location:   "Remote - Germany",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAccept,
			wantRule:   "Germany",
		},
		{
			name:       "mixed-case and dashed compound",
			location:   "EU-EMEA",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAccept,
			wantRule:   "EMEA",
		},
		{
			name:       "portals own remote tag reaches the scorer",
			location:   "Remote (Global)",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAmbiguous,
			wantRule:   "remote",
		},
		{
			// "Worldwide" needed its own allow entry before this change,
			// and portals that worded it differently were missed.
			name:       "worldwide reaches the scorer",
			location:   "Worldwide",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAmbiguous,
			wantRule:   "worldwide",
		},
		{
			name:       "bare remote reaches the scorer",
			location:   "Remote",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAmbiguous,
			wantRule:   "remote",
		},
		{
			name:       "fully remote phrase reaches the scorer",
			location:   "Fully Remote",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAmbiguous,
			wantRule:   "remote",
		},
		{
			name:       "anywhere reaches the scorer",
			location:   "Anywhere",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAmbiguous,
			wantRule:   "anywhere",
		},
		{
			name:       "work from home reaches the scorer",
			location:   "Work from home",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAmbiguous,
			wantRule:   "work from home",
		},
		{
			// Umlauts normalize to a word break, so the marker has to be
			// matched after normalization, not by byte comparison.
			name:       "german remote word reaches the scorer",
			location:   "Ortsunabhängig",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAmbiguous,
			wantRule:   "ortsunabhängig",
		},
		{
			// A remote posting that does name a foreign country is the AI
			// scorer's call, not the pre-filter's: it is the only part of
			// the pipeline holding the relocation rule. Under the old
			// deny-list-only config this posting was scored too, but only
			// because every country had to be enumerated by hand.
			name:       "remote canada reaches the scorer",
			location:   "Remote - Canada",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAmbiguous,
			wantRule:   "remote",
		},
		{
			name:       "empty location reaches the scorer",
			location:   "",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAmbiguous,
		},
		{
			// A portal that cannot express a location must not have its
			// whole inventory dropped for free.
			name:       "filler location reaches the scorer",
			location:   "N/A",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAmbiguous,
			wantRule:   "n a",
		},
		{
			name:       "unspecified location reaches the scorer",
			location:   "Various",
			cfg:        personal,
			wantPassed: true,
			wantKind:   LocationKindAmbiguous,
			wantRule:   "various",
		},
		{
			// The whitelist working as intended: naming a foreign place is
			// enough to be dropped, with no deny list to keep in sync.
			name:       "onsite canada dropped",
			location:   "Toronto, Canada",
			cfg:        personal,
			wantPassed: false,
			wantKind:   LocationKindUnmatched,
		},
		{
			name:       "onsite japan dropped",
			location:   "Tokyo, Japan",
			cfg:        personal,
			wantPassed: false,
			wantKind:   LocationKindUnmatched,
		},
		{
			// The boundary rule again, this time through the whole check: a
			// "US" entry must not quietly accept Australia.
			name:       "us entry does not accept australia",
			location:   "Sydney, Australia",
			cfg:        config.LocationConfig{Accept: []string{"US"}},
			wantPassed: false,
			wantKind:   LocationKindUnmatched,
		},
		{
			name:       "us entry accepts a us location",
			location:   "Austin, US",
			cfg:        config.LocationConfig{Accept: []string{"US"}},
			wantPassed: true,
			wantKind:   LocationKindAccept,
			wantRule:   "US",
		},
		{
			// unmatched: pass is the maximum-coverage escape hatch: even a
			// foreign onsite posting gets scored.
			name:       "unmatched pass sends a foreign onsite posting to the scorer",
			location:   "Toronto, Canada",
			cfg:        config.LocationConfig{Accept: []string{"Germany"}, Unmatched: config.LocationUnmatchedPass},
			wantPassed: true,
			wantKind:   LocationKindUnmatched,
		},
		{
			name:       "empty accept disables the location filter",
			location:   "Toronto, Canada",
			cfg:        config.LocationConfig{},
			wantPassed: true,
			wantKind:   LocationKindDisabled,
		},
		{
			name:       "remote still reaches the scorer when accept is empty",
			location:   "Remote",
			cfg:        config.LocationConfig{},
			wantPassed: true,
			wantKind:   LocationKindDisabled,
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
	cfg := config.LocationConfig{Accept: []string{"  GERMANY  ", "European Union"}}

	if got := MatchLocation(model.Job{Location: "berlin, germany"}, cfg); !got.Passed || got.Kind != LocationKindAccept {
		t.Errorf("expected untrimmed, uppercase accept entries to match, got %+v", got)
	}
	if got := MatchLocation(model.Job{Location: "TORONTO, CANADA"}, cfg); got.Passed {
		t.Errorf("expected an unlisted country to be dropped, got %+v", got)
	}
}

func TestMatchLocation_MarkerBoundaries(t *testing.T) {
	// Markers are matched on word boundaries, so a location must not become
	// ambiguous just because it contains a marker as a substring.
	cfg := config.LocationConfig{Accept: []string{"Germany"}}

	for _, location := range []string{"Remoteville, Germany", "Virtuality Park, Germany"} {
		got := MatchLocation(model.Job{Location: location}, cfg)
		if got.Kind != LocationKindAccept {
			t.Errorf("MatchLocation(%q).Kind = %q, want %q", location, got.Kind, LocationKindAccept)
		}
	}
}

func TestMatchLocation_LongLocationString(t *testing.T) {
	// Postings in the wild carry long location strings ("Berlin, Germany;
	// London, United Kingdom; Remote - Canada"). Naming an accepted place
	// anywhere in that string is enough to reach the scorer, and the scorer
	// is where the rest of the list gets judged against the profile.
	cfg := config.LocationConfig{
		Accept:    []string{"Germany"},
		Unmatched: config.LocationUnmatchedReject,
	}

	location := "Berlin, Germany; London, United Kingdom; Remote - Canada"
	if got := MatchLocation(model.Job{Location: location}, cfg); !got.Passed {
		t.Errorf("expected multi-region posting listing Germany to reach the scorer, got %+v", got)
	}

	// Same shape, without the accepted region: no remote marker to save it,
	// so it is dropped cheaply.
	location = "London, United Kingdom; Toronto, Canada"
	if got := MatchLocation(model.Job{Location: location}, cfg); got.Passed {
		t.Errorf("expected multi-region posting without Germany to be dropped, got %+v", got)
	}

	location = strings.Repeat("Berlin, Germany; ", 20)
	if got := MatchLocation(model.Job{Location: location}, cfg); !got.Passed {
		t.Errorf("expected Germany-only posting to pass, got %+v", got)
	}
}
