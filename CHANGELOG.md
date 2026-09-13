# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html)
as described in "Versioning and releases" in the README. The config file schema
is the public API that version numbers speak to.

## [Unreleased]

### Breaking

- **`filter.locations.deny` is removed and `filter.locations.allow` is renamed
  to `filter.locations.accept`.** `accept` is now a whitelist of the places you
  can legally work from, and a place you cannot work from no longer needs
  listing:

  ```yaml
  filter:
    locations:
      accept:
        - Germany
        - EMEA
        - European Union
      unmatched: reject
  ```

  Both old keys fail at startup with an error naming the replacement, rather
  than being silently ignored. Why: with `unmatched: pass`, which the previous
  documented example used, `allow` could not change the outcome of a single
  posting - only `deny` filtered - so every country that should have been
  excluded had to be enumerated by hand.

### Changed

- **Remote and unlabelled locations always reach the AI scorer.** An empty
  location, a remote phrasing (`remote`, `worldwide`, `anywhere`, `home
  office`, `work from home`, `ortsunabhängig`, ...), and a filler value like
  `N/A` are all handed to the scorer, and `unmatched` no longer applies to
  them. They are matched as whole words against a built-in list instead of
  having to be enumerated in config, because the phrasings a portal might use
  for "anywhere" are not a closed set and the pre-filter must not guess. A
  posting located `"Remote - Canada"` therefore reaches the scorer, where the
  profile's relocation rules judge it, instead of needing a `deny` entry.
- **`filter.locations.unmatched` now means "names a place that is not
  accepted"** - a foreign onsite posting - rather than "matched no list". Its
  default remains `reject`.
- **`filter.keywords` is documented as a recall gate rather than a ranking.**
  Listing adjacent titles (`sre`, `devops`) widens the net without lowering the
  bar, because how much a posting is wanted is expressed in `ai.profile`. No
  schema or matching change.

### Added

- **A built-in ambiguous-location list**, so a location that is remote or
  says nothing about a country reaches the scorer in any phrasing, with the
  deciding marker recorded as the filter's `Rule` in the debug log and the
  end-of-run sample.
- **`config/config.example.yaml` and `deploy/configmap.example.yaml`** document
  the new `accept` shape and the recall-versus-precision split between
  `filter.keywords` and `ai.profile`.
- **A shared outbound HTTP client that identifies the tool and paces itself**
  (`internal/httpclient`). Every connector now sends
  `User-Agent: go-get-a-job (+https://github.com/Erik-Schuetze/go-get-a-job)`,
  keeps at least 250ms between requests (globally, so a concurrent per-posting
  fan-out is serialized rather than able to arrive as a burst), obeys a `429`
  or `503` by waiting out its `Retry-After` (capped at 30s) for up to two
  retries, and refuses to exceed 10,000 requests in a run. These hosts are
  other people's infrastructure, published for browsers rather than for a
  polling client; the pacing is what keeps a wide fan-out on a large board from
  being indistinguishable from a small flood. See "Request etiquette" in the
  README.

## [0.2.0] - 2026-09-12

The theme of this release is that the wrong jobs got through, and that the
config that decides what gets through did not belong in a public repository.
Both are fixed.

### Breaking

- **`filter.locations` is now a mapping, not a list.** The old form was a
  single OR-matched list of substrings, which cannot express "anywhere except
  here". It is now an allow list, a deny list, and a decision for everything
  else:

  ```yaml
  filter:
    locations:
      allow:
        - "Germany"
        - "EMEA"
        - "Remote (Global)"
      deny:
        - "Canada"
        - "United States"
      unmatched: reject
  ```

  A config still using the list form fails at startup with an error naming the
  key, rather than silently filtering differently.

### Changed

- **Location matching is word-boundary aware instead of substring based.**
  A list entry now has to match a whole word (or whole phrase) in the
  posting's location, so `"US"` matches `"Austin, US"` and `"Remote - US"`
  but no longer matches `"Australia"`, `"Belarus"`, or `"Prussia"`. Matching
  also normalizes separators first, so `"Remote - Canada"`, `"remote;canada"`,
  and `"REMOTE / CANADA"` are all the same location.
- **`deny` wins over `allow`.** A posting is rejected when it matches any
  `deny` entry, even when an `allow` entry also matches. This is what makes a
  broad `allow` entry like `"Remote"` safe to write, and it is the mechanism
  that stops a posting located `"Remote - Canada"` from riding in on it.
- **The scorer treats a profile's location and relocation constraints as hard
  filters.** A posting that requires being based outside the places the
  profile accepts, or that requires relocating there, now scores 0.2 or below
  regardless of how well the technology stack matches. Remote roles are
  judged as the place they are restricted to, not as a separate category.
- **`config/config.example.yaml` and the example manifests describe a generic
  persona, not the maintainer's.** Real configuration belongs in your own
  private repository; see "Configuring what it watches" in the README.

### Removed

- **`deploy/configmap.yaml`.** It held the maintainer's real profile, watched
  companies, and location preferences in a public repository. It is replaced
  by `deploy/configmap.example.yaml`, which carries the same structure with a
  generic "Go developer" persona, so `deploy/` is still a working example.

### Added

- **`filter.locations.deny` and `filter.locations.unmatched`.** `unmatched`
  is `reject` (the default) or `pass`, and decides what happens to a location
  that matches neither list - typically a bare `"Remote"`, which says nothing
  about where you may legally be based. `pass` hands those to the AI scorer,
  at one AI call each.
- **`ai.instructions`** - operator-authored rules appended to the scoring
  prompt, for hard constraints that would clutter the free-text profile.
  Documented as trusted text: it is added to the system prompt, not to the
  untrusted fenced-off posting, so untrusted text must never be pasted into
  it.
- **`--log-level`** (`debug`, `info`, `warn`, `error`; default `info`).
  `debug` logs one line per posting the location pre-filter drops, naming the
  entry and whether it was `deny` or `unmatched`; every run also reports a
  `filtered_by_location` count and a bounded sample of what it dropped, at
  `info`. Previously a dropped posting left no trace at all, which is how a
  posting in the wrong country went unnoticed.
- **Validation and bounds for the new config values**: `unmatched` must be
  `reject` or `pass`; location entries may not be blank, shorter than two
  characters, or longer than 100 characters, and there may be at most 200 per
  list; `ai.instructions` is capped at 4000 characters. All of these fail at
  startup with a specific message rather than misbehaving at run time.
- **`CHANGELOG.md`** and the "Versioning and releases" section of the README,
  which state the versioning policy the project follows.

### Fixed

- A posting in a location the operator cannot work from is no longer scored
  and notified just because its location string happened to contain a word
  from the allow list.

### Notes

- Tightening the filter does not re-examine anything already recorded. A
  posting is written to the store as soon as it is first seen, so jobs that
  were accepted and notified about under the old rules are not re-scored or
  withdrawn.

[Unreleased]: https://github.com/Erik-Schuetze/go-get-a-job/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/Erik-Schuetze/go-get-a-job/releases/tag/v0.2.0
