// Package httpbody bounds how much of an HTTP response body this tool is
// willing to read into memory.
//
// Every response the pipeline reads comes from a third party - a job board's
// API, an AI provider, or the ntfy server. The HTTP client timeouts in this
// codebase bound how *long* one of those can take, but not how *much* it can
// send: a server that streams an unbounded body for 30 seconds is still
// bounded only by its own bandwidth, and a single such body is enough to
// exhaust the CronJob's memory limit and take the run down. Applying a size
// cap alongside the timeout closes that gap.
//
// This is defense-in-depth rather than a fix for a known bug - none of the
// endpoints involved are expected to return anything close to these limits,
// which is exactly why exceeding one is worth reporting loudly instead of
// silently truncating.
package httpbody

import (
	"encoding/json"
	"fmt"
	"io"
)

// Content limits, chosen per call site so that "unexpectedly large" means
// something in every context.
const (
	// MaxAPIBytes covers a whole board's worth of postings in one response.
	// Large boards with full descriptions inline (Greenhouse is fetched with
	// content=true) are the biggest legitimate response in the pipeline and
	// are still an order of magnitude below this.
	MaxAPIBytes int64 = 8 << 20 // 8 MiB

	// MaxJSONBytes covers a small, structured response that is expected to
	// be a few kilobytes at most: a chat-completion result, one posting's
	// detail record, or a page of posting IDs.
	MaxJSONBytes int64 = 1 << 20 // 1 MiB

	// MaxErrorBytes covers an error body that is only ever echoed into a
	// log line. Reading more than this from a failing endpoint buys nothing.
	MaxErrorBytes int64 = 8 << 10 // 8 KiB
)

// TooLargeError reports that a response body exceeded its size cap, as
// distinct from a body that was under the cap but malformed. The two want
// different log lines - a decoding error means the endpoint changed shape,
// while this one means it is sending something this tool will not read.
type TooLargeError struct {
	// Limit is the cap, in bytes, that the body exceeded.
	Limit int64
}

func (e *TooLargeError) Error() string {
	return fmt.Sprintf("response body exceeded %d byte limit", e.Limit)
}

// ReadAll reads r in full, but fails with a *TooLargeError rather than
// growing past limit. A limit of 0 or less applies DefaultLimit.
//
// It reads limit+1 bytes before deciding, so a body of exactly limit is
// accepted and only a strictly larger one is rejected - the limit is a
// ceiling on what gets buffered, not a guess about where the body ends.
func ReadAll(r io.Reader, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = MaxJSONBytes
	}

	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, &TooLargeError{Limit: limit}
	}
	return b, nil
}

// DecodeJSON reads at most limit bytes from r and unmarshals them into v.
//
// Callers should wrap the returned error themselves to say which endpoint
// failed; the error is already distinguishable via errors.As if a caller
// wants to treat "too large" specially.
func DecodeJSON(r io.Reader, limit int64, v any) error {
	b, err := ReadAll(r, limit)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("decoding JSON: %w", err)
	}
	return nil
}
