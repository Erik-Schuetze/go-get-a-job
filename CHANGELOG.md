## [0.5.1] - 2026-09-13

A bug-fix release for the one defect the first real batch exposed: a posting
advertised in more than one place was announced as whichever place the board
happened to list first, which is sometimes the one place that rules it out.

### Fixed

- **A multi-location posting is announced as a place you can work from, not as
  the board's first.** `locationHint` cut the location at the first `;` and
  showed only that segment, on the documented assumption that "the first segment
  is the one the board leads with, so it is both the most representative and the
  shortest". Canonical falsifies it. Its board stores `Home Based - Americas;
  Home based - EMEA` and leads with the **Americas** entry, so the notification
  read:

  ```
  💼 Canonical: Software Engineer - Python/Golang - Kubernetes · Home Based - Americas
  ```

  only to show `Home based - EMEA` when the posting was opened - a false
  negative manufactured in the one place it cannot be corrected from context.
  The rest of the pipeline was right: the location whitelist passed the string
  because `EMEA` is accepted, and the scorer kept `locationOk` true, since a
  posting only fails that when *every* listed location rules you out.

  The fix stops guessing in the notify layer. `LocationDecision.Rule`
  (`internal/filter/location.go`) already records which `accept` entry selected
  the posting, and it now reaches the notification as
  `notify.Match.LocationRule`, which picks the segment naming that entry. Two
  details are deliberate:

  - **Only an `accept` entry is forwarded.** `Rule` is also set for the
    ambiguous markers that hand a posting to the scorer (`remote`, `worldwide`,
    `n a`), and those are the *opposite* of a place. Forwarding one would have
    the notifier hunt for a segment containing the word "remote" - which, on a
    remote posting, is every segment, so the first would win by accident and the
    fix would silently do nothing.
  - **A miss falls back to the first segment.** The rule is located by a
    case-insensitive substring test, which is looser than the filter's
    word-boundary match and free to miss: by the time this runs, the filter has
    already decided the entry applies to the whole string, and the only question
    left is which part of it carries the entry. When the rule is absent or gone
    from the string, the previous behaviour stands, because there is nothing
    better to go on.

  Measured against the 3,792 stored postings: **108 of the 448 multi-location
  postings render differently, and every one of them is an improvement** - the
  two delivered Canonical notifications above, 20 more Canonical postings
  leading with `Home Based - APAC` or `Americas` instead of their EMEA entry,
  JetBrains postings leading with `Amsterdam, Netherlands` or `Belgrade,
  Serbia` instead of the `Berlin, Germany` entry that made them matches, and
  Datadog, Chainguard and GitLab postings leading with a third country instead
  of `Germany`. No posting renders worse, which follows from the segment now
  being chosen by the entry that made it acceptable.

  This changes nothing about *what* matches, only how it is read: no posting is
  newly included, excluded, re-scored or re-notified.

- **`deploy/cronjob.yaml` and the README no longer disagree about which release
  an image digest belongs to.** The manifest pinned `0.1.0` with its real digest
  while the README's "move to a new release" snippet paired `v0.2.0` with that
  same digest, so following the documentation literally would have pinned one
  release's tag to another release's image. Both now name the same release and
  the same digest, and the README says where each value comes from.

## [0.5.0] - 2026-09-13

The notification is the only part of this program anyone actually reads, and it
was the least considered: a fixed briefcase icon, a location on its own labelled
line, and a score that only existed as prose. It is now built around what a
collapsed phone notification shows. Location also stops being a scoring input,
which is the part that can change what you are told about.

### Breaking

- **Location is answered in `locationOk`, not by lowering the score.** The
  scorer returns it as its own field, and a `false` saves the posting without
  notifying you. This matters for an existing config: `ai.instructions` or
  `ai.profile` text that says "score it 0.2 or below" for a location now
  **contradicts** the fixed prompt, which says a location constraint may never
  move the score. Operator text is appended after the contract, so it wins by
  position - whichever rule the model settles on per posting, the outcome is
  undefined. Rewrite such rules to use `locationOk`:

  ```yaml
  ai:
    instructions: |
      Requires being based outside Germany, or relocating out of it ->
      locationOk: false. The score is unaffected either way.
  ```

  Why: a rule phrased as a score makes "wrong job" and "right job, wrong
  country" arrive looking identical, which is exactly the notification you
  cannot act on.
- **`locationOk` is absent-safe, deliberately.** A `false` is only read from an
  explicit `false`; a missing, null, or malformed field means OK. A plain
  boolean would decode to `false` on an omitted field, so one truncated reply
  would silently discard the posting - the missed-opening failure this tool
  exists to prevent.

### Added

- **`notify.ntfy.matchTiers`, so the emoji is the score.** Score bands map to an
  emoji and an ntfy priority, which makes the list skimmable without opening
  anything. Clients expose one notification channel per priority, so the low
  tiers can be muted while the top one still rings. Omit the block and the
  defaults apply, so an existing config is unaffected:

  ```yaml
  notify:
    ntfy:
      matchTiers:
        - minScore: 0.95
          emoji: "💎"
          priority: 5
        - minScore: 0.85
          emoji: "⭐"
          priority: 4
        - emoji: "💼"          # catch-all; minScore intentionally absent
          priority: 3
  ```

  Tiers are matched top-down, so they are listed highest-first and the last
  entry must omit `minScore`. A tier whose `minScore` is below
  `filter.minAIScore` can never be reached, which is a warning at startup rather
  than an error - the only other symptom would be an emoji that never arrives.
- **The location is in the notification title**, where a collapsed notification
  shows it: `💎 Grafana Labs: Platform Engineer · Germany`.
- **A metadata line, from data every connector was already fetching and
  discarding.** `Department`, `WorkplaceType` and `EmploymentType` from the
  board, `PostedAt` as `Posted 6 days ago`, and the `signals[]` the scorer was
  already returning and the notifier was throwing away, rendered as
  `Matched: Crossplane, Terraform`. A field a board does not publish is omitted
  rather than rendered as `N/A`.

### Changed

- **The company is now a filterable tag instead of an icon.** ntfy turns a tag
  matching an emoji short code into an emoji *prepended to the title*, so the
  old `Tags: briefcase` header was the only reason 💼 ever appeared - and
  keeping it alongside a tier emoji would put two emoji on every notification.
  The company slug (`grafana_labs`) matches no short code and renders as a
  filterable label underneath instead. The slug is sanitized rather than
  cosmetic: only `[a-z0-9_]` is emitted, so a comma in `Solo.io, Inc` cannot
  split the `Tags` header into two tags.
- **Title truncation reserves the location before cutting the title.** The
  location suffix is budgeted for up front, and the 3-rune `...` marker is part
  of that budget. Truncating the assembled string instead deletes the location -
  but only on long titles, which is the worst way for it to fail.
- **Greenhouse departments are decoded.** `departments[].name` had no decoding
  at all, so the field was never populated for any Greenhouse board. Ashby,
  Lever, Workable, Personio, Teamtailor, SmartRecruiters and Recruitee now
  populate the same fields from their own responses.
- **A vetoed posting is counted.** Drops are reported as `vetoed_by_location` in
  the run summary and the run-complete log line, because a drop with no trace
  cannot be told apart from a posting the watcher never saw.

## [0.4.0] - 2026-09-13

Two themes: **reach more employers** (four new connectors) and **notice when one
goes dark** (`-validate` plus the dead-source guard). Also in here, from PR #8:
every outbound request now identifies the tool and is paced.

### Added

- **Four more connectors: Personio, Recruitee, Teamtailor, and Workable.**
  Every one is a config-only addition - `type:` plus `company:` - and all four
  were verified against live boards (Contabo, xneelo, Spacelift, Hugging Face).
  Personio matters most for a Germany-based search, since it is the ATS many
  mid-size German employers use; its feed is XML rather than JSON, so it is the
  first connector that does not use the shared JSON decoder.
- **Personio reports a wrong company name as what it is.** An unknown Personio
  company does not 404: it redirects to Personio's own marketing site, so the
  body is HTML and decoding it as XML yields an error that reads like a schema
  change. The connector checks for the feed's root element first and says
  "wrong company subdomain, or the board moved" instead.
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
- **A `guard:` config block, and a warning when a configured board stops
  returning postings.** Every successful fetch is recorded per source, and a
  source that has returned nothing for `deadSourceRuns` consecutive runs
  (default **14**) produces one `ntfy` warning - repeated every further
  `deadSourceRuns` runs while it stays silent, not on every run. The point is a
  silent failure mode: a board that is renamed, migrated to another ATS, or
  starts rejecting anonymous requests returns a *successful* empty list at
  several providers, which looks exactly like a company that has stopped
  hiring. `minRequestIntervalMs` and `maxRequestsPerRun` are now set from
  config too, rather than being compile-time constants.
- **A `-validate` flag**, which fetches every configured source once, prints
  per-source health (fetch errors, empty boards, postings missing an ID or
  title), and exits non-zero if any source looks unhealthy. No AI calls, no
  database, no notifications. It exists because a wrong board slug is
  otherwise silent: SmartRecruiters answers *any* slug with
  `200 {"totalFound":0}`, so a typo there is indistinguishable from an idle
  board in the logs of a normal run. See "Verifying a board before you add it"
  in the README.

### Changed

- **Teamtailor postings are located by their title when that is the only place
  they say it.** Teamtailor boards routinely file a "Remote, European Union"
  role under the company's registered office and emit no other signal - neither
  `jobLocationType` nor `applicantLocationRequirements` was present on any
  posting checked. Read alone, `jobLocation` labelled exactly those roles
  "Warsaw, PL", so a Germany-scoped `accept` list would have rejected the
  postings most worth finding. The trailing remote parenthetical in the title
  is now part of `Location`, ahead of the structured place.
- **Recruitee and Workable locations are de-duplicated by segment.**
  Recruitee spreads location over three overlapping fields (`location` is
  usually "City, Country" while `city` and `country` repeat its halves), so
  rendering them naively produced "Leipzig, Germany, Leipzig, Germany" and let
  a single `accept` entry match twice.
- **`Source` gained a `Label()` method** (`greenhouse/grafanalabs`,
  `workday/suse/Jobsatsuse`, ...), distinct from `Name()`, which stays the
  connector type. A config watching six Greenhouse boards is six sources with
  one `Name()`, so per-source state keyed on `Name()` would merge their
  histories and let five healthy boards mask a sixth that had gone quiet. This
  is an interface change for anyone implementing a connector outside this repo;
  the shipped connectors all implement it.
- **Only successful fetches count toward the dead-source streak.** A failed
  fetch is already reported per-source, and counting it would let one flaky
  network day push a healthy board toward a false alarm - precisely the warning
  an operator learns to ignore.

### Fixed

- **A run that failed partially now exits non-zero.** `main` returned `0`
  unless *every* source failed, so a cron run in which several sources errored
  looked successful to `kubectl` and to any future alerting on the job status.
  A source error or a post-processing error now exits `1`; the "run failed"
  notification is still reserved for a total failure, so a single flaky board
  does not page.

## [0.3.0] - 2026-09-12

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

[Unreleased]: https://github.com/Erik-Schuetze/go-get-a-job/compare/v0.5.1...HEAD
[0.5.1]: https://github.com/Erik-Schuetze/go-get-a-job/releases/tag/v0.5.1
[0.5.0]: https://github.com/Erik-Schuetze/go-get-a-job/releases/tag/v0.5.0
[0.4.0]: https://github.com/Erik-Schuetze/go-get-a-job/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/Erik-Schuetze/go-get-a-job/releases/tag/v0.3.0
[0.2.0]: https://github.com/Erik-Schuetze/go-get-a-job/releases/tag/v0.2.0
