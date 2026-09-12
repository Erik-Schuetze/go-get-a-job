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
		name      string
		locations []string
		want      bool
	}{
		{"empty locations always match", nil, true},
		{"matches case-insensitively", []string{"GERMANY"}, true},
		{"matches remote", []string{"remote"}, true},
		{"no match", []string{"France", "Spain"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := LocationMatch(job, tt.locations); got != tt.want {
				t.Errorf("LocationMatch() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPasses(t *testing.T) {
	job := model.Job{
		Title:       "Platform Engineer",
		Description: "Crossplane and Terraform all day.",
		Location:    "Remote, Germany",
	}

	cfg := config.FilterConfig{
		Keywords:  []string{"crossplane"},
		Locations: []string{"Germany"},
	}
	if !Passes(job, cfg) {
		t.Error("expected job to pass both keyword and location filters")
	}

	cfg.Locations = []string{"France"}
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
