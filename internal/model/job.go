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

	// The three fields below are short, human-readable labels several ATS
	// APIs return alongside a posting. They cost nothing to carry (no extra
	// request, no extra AI token) and they are the difference between two
	// notifications for the same job title at the same company being
	// distinguishable at a glance on a phone.
	//
	// All three are free-form text from a third party, so they are for
	// display only: nothing in the filter or scoring path may read them,
	// and anything rendering them must sanitize first.

	// Department is the employer's own grouping, e.g. "R&D: Platform".
	Department string
	// WorkplaceType describes where the work happens, e.g. "Remote",
	// "Hybrid", "Onsite". Distinct from Location, which names *where*.
	WorkplaceType string
	// EmploymentType is the engagement, e.g. "Full-time", "Contract".
	EmploymentType string

	// PostedAt is best-effort; some sources (e.g. Workday's list endpoint)
	// only expose a relative string rather than a precise timestamp, in
	// which case this is left as the zero value. Callers should rely on
	// the store's first_seen_at for "new to us" freshness instead of
	// PostedAt for anything that must be reliably comparable.
	PostedAt time.Time
}
