package sources

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Erik-Schuetze/go-get-a-job/internal/model"
	"github.com/Erik-Schuetze/go-get-a-job/internal/sanitize"
)

// validateMaxListedJobIssues bounds how many per-posting problems one source
// is allowed to spell out, so a systematically broken board prints a line
// rather than a wall of text.
const validateMaxListedJobIssues = 3

// Validate fetches each source once and reports the ones that do not look
// healthy. It is the `-validate` mode of the CLI: it makes no AI calls, touches
// no database, and sends no notifications, but it does perform the same
// requests a real run would, because "is this board fetchable?" is only
// answerable by fetching it.
//
// It exists because a wrong board slug is otherwise silent. Most ATS APIs
// answer a nonexistent board with an HTTP error, which a run reports as a
// source error - loud enough. SmartRecruiters instead answers *any* slug with
// `200 {"totalFound":0}`, so a typo there behaves exactly like a company that
// has stopped hiring, and the only way to tell the difference is to decide
// what "no postings" means for a board the operator believes is active.
//
// Sources are checked one at a time, in config order. That is not an
// implementation detail: the shared outbound client paces requests anyway, and
// validating a config means hitting every one of these companies, so nothing
// here runs in parallel.
//
// Returns true when every source looks healthy.
func Validate(ctx context.Context, logger *slog.Logger, srcs []Source) bool {
	healthy := true
	checked := 0

	for _, src := range srcs {
		label := sanitize.SingleLine(src.Label(), 200)

		if ctx.Err() != nil {
			// Stopped deliberately (SIGINT) or hit a deadline. Reporting the
			// remaining sources as unhealthy would be a lie, so the run is
			// reported as inconclusive instead.
			logger.Warn("validation inconclusive: stopped before checking every source",
				"checked", checked, "of", len(srcs), "reason", ctx.Err())
			return false
		}
		checked++

		jobs, err := src.Fetch(ctx)
		if err != nil {
			healthy = false
			logger.Error("unhealthy: fetch failed",
				"source", label,
				"error", sanitize.SingleLine(err.Error(), 500),
			)
			continue
		}

		if len(jobs) == 0 {
			healthy = false
			logger.Warn("unhealthy: board returned no postings",
				"source", label,
				"hint", "either the company has no open roles, or the board identifier is wrong - check it in a browser",
			)
			continue
		}

		// A board can also be *half* broken: it answers, it returns rows, and
		// every row is missing the fields the pipeline needs (an ID to dedupe
		// on, a title to notify with). That parses fine and produces nothing,
		// so it is checked explicitly.
		if problems := jobProblems(jobs); len(problems) > 0 {
			healthy = false
			logger.Warn("unhealthy: postings are missing required fields",
				"source", label,
				"postings", len(jobs),
				"problems", problems,
			)
			continue
		}

		logger.Info("healthy", "source", label, "postings", len(jobs))
	}

	if healthy {
		logger.Info("every source looks healthy", "sources", checked)
	} else {
		logger.Warn("some sources need attention; see the lines above")
	}
	return healthy
}

// jobProblems returns bounded, human-readable descriptions of postings that
// cannot be used downstream.
func jobProblems(jobs []model.Job) []string {
	var problems []string
	for i, job := range jobs {
		var missing []string
		if job.ID == "" {
			missing = append(missing, "id")
		}
		if job.Title == "" {
			missing = append(missing, "title")
		}
		if len(missing) == 0 {
			continue
		}
		problems = append(problems, fmt.Sprintf("postings[%d] (%s) is missing %s", i, job.Source, strings.Join(missing, ", ")))
		if len(problems) >= validateMaxListedJobIssues {
			break
		}
	}
	return problems
}
