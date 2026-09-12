// Package filter implements the two-stage relevance pipeline: a cheap
// keyword/location pre-filter that runs on every fetched job for free,
// and an AI relevance scorer (called only for jobs that pass the
// pre-filter) that judges fit against a free-text user profile.
package filter

import (
	"strings"

	"github.com/Erik-Schuetze/go-get-a-job/internal/config"
	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
)

// KeywordMatch reports whether job's title or description contains at
// least one of the given keywords (case-insensitive substring match). An
// empty keywords list disables the filter (always matches) - useful for
// sending everything straight to the AI scorer, at higher cost.
func KeywordMatch(job model.Job, keywords []string) bool {
	if len(keywords) == 0 {
		return true
	}
	haystack := strings.ToLower(job.Title + "\n" + job.Description)
	for _, kw := range keywords {
		kw = strings.TrimSpace(kw)
		if kw == "" {
			continue
		}
		if strings.Contains(haystack, strings.ToLower(kw)) {
			return true
		}
	}
	return false
}

// LocationMatch reports whether job's location contains at least one of
// the given location substrings (case-insensitive). An empty locations
// list disables the filter (always matches).
func LocationMatch(job model.Job, locations []string) bool {
	if len(locations) == 0 {
		return true
	}
	haystack := strings.ToLower(job.Location)
	for _, loc := range locations {
		loc = strings.TrimSpace(loc)
		if loc == "" {
			continue
		}
		if strings.Contains(haystack, strings.ToLower(loc)) {
			return true
		}
	}
	return false
}

// Passes reports whether job should proceed to AI scoring, applying both
// the keyword and location pre-filters from cfg. This is the cheap gate
// that keeps AI usage (and cost) down to only plausibly-relevant postings.
func Passes(job model.Job, cfg config.FilterConfig) bool {
	return KeywordMatch(job, cfg.Keywords) && LocationMatch(job, cfg.Locations)
}
