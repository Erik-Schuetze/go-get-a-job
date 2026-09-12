// Package model holds the core domain types shared across the go-get-a-job
// pipeline (sources -> filter -> store -> notify).
package model

import "time"

// Job represents a single job posting, normalized from whatever source
// (Greenhouse, Lever, Ashby, SmartRecruiters, Workday, ...) it came from.
type Job struct {
	// ID is a stable, globally unique identifier for this posting, derived
	// from Source + Company + the source's own posting ID. It is used as
	// the primary key in the store for deduplication across runs.
	ID string

	// Source is the connector that produced this job, e.g. "greenhouse",
	// "lever", "ashby", "smartrecruiters", "workday".
	Source string

	// Company is the human-readable display name from config (e.g.
	// "Grafana Labs"), not necessarily the source's internal slug/token.
	Company string

	Title       string
	Location    string
	URL         string
	Description string

	// PostedAt is best-effort; some sources (e.g. Workday's list endpoint)
	// only expose a relative string rather than a precise timestamp, in
	// which case this is left as the zero value. Callers should rely on
	// the store's first_seen_at for "new to us" freshness instead of
	// PostedAt for anything that must be reliably comparable.
	PostedAt time.Time
}
