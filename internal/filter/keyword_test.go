package filter

import (
	"strings"
	"testing"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

func TestKeywordMatch(t *testing.T) {
	job := model.Job{Title: "Senior Platform Engineer", Description: "You'll own our Crossplane compositions."}

	tests := []struct {
		name     string
		keywords []string
		want     bool
	}{
		{"empty keywords always match", nil, true},
		{"matches title case-insensitively", []string{"PLATFORM ENGINEER"}, true},
		{"matches description", []string{"crossplane"}, true},
		{"no match", []string{"sales", "marketing"}, false},
		{"one of several matches", []string{"marketing", "crossplane"}, true},
		{"blank keywords ignored", []string{"", "  "}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := KeywordMatch(job, tt.keywords); got != tt.want {
				t.Errorf("KeywordMatch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLocationMatch(t *testing.T) {
	job := model.Job{Location: "Berlin, Germany (Remote)"}

	tests := []struct {
		name string
		cfg  config.LocationConfig
		want bool
	}{
		{"empty accept list always matches", config.LocationConfig{}, true},
		{"matches case-insensitively", config.LocationConfig{Accept: []string{"GERMANY"}}, true},
		{"a remote signal always matches", config.LocationConfig{Accept: []string{"France"}}, true},
		{"unmatched pass lets it through", config.LocationConfig{Accept: []string{"France"}, Unmatched: config.LocationUnmatchedPass}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MatchLocation(job, tt.cfg).Passed; got != tt.want {
				t.Errorf("MatchLocation() = %v, want %v", got, tt.want)
			}
		})
	}

	// A location that names a country the list does not accept, with no
	// remote signal to make it ambiguous, is what the unmatched mode is for.
	onsite := model.Job{Location: "Toronto, Canada"}
	if MatchLocation(onsite, config.LocationConfig{Accept: []string{"Germany"}}).Passed {
		t.Error("expected an unlisted onsite location to be rejected by default")
	}
	if !MatchLocation(onsite, config.LocationConfig{Accept: []string{"Germany"}, Unmatched: config.LocationUnmatchedPass}).Passed {
		t.Error("expected unmatched: pass to send an unlisted onsite location to the scorer")
	}
}

func TestPasses(t *testing.T) {
	job := model.Job{
		Title:       "Platform Engineer",
		Description: "Crossplane and Terraform all day.",
		Location:    "Berlin, Germany",
	}

	cfg := config.FilterConfig{
		Keywords:  []string{"crossplane"},
		Locations: config.LocationConfig{Accept: []string{"Germany"}},
	}
	if !Passes(job, cfg) {
		t.Error("expected job to pass both keyword and location filters")
	}

	cfg.Locations = config.LocationConfig{Accept: []string{"Canada"}}
	if Passes(job, cfg) {
		t.Error("expected job to fail location filter")
	}
}

func TestKeywordMatch_LongDescriptionSanity(t *testing.T) {
	// Regression guard: make sure matching works across a title+description
	// join even when the keyword only appears deep in a long description.
	job := model.Job{
		Title:       "Engineer",
		Description: strings.Repeat("filler ", 500) + "mentions crossplane here",
	}
	if !KeywordMatch(job, []string{"crossplane"}) {
		t.Error("expected match on keyword buried in long description")
	}
}
