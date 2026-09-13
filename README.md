# go-get-a-job

A small, self-hosted niche job watcher. It polls a curated list of
companies' own Applicant Tracking Systems (Greenhouse, Lever, Ashby,
SmartRecruiters, Workday) — not generic job boards — filters new postings
with a cheap keyword pass plus an AI relevance score (DeepSeek, or any
OpenAI-compatible provider), and pushes matches to your phone via
[ntfy](https://ntfy.sh). It runs as a Kubernetes CronJob once a day (or
whatever schedule you choose) and needs no frontend — everything is
configured through one YAML file.

## Why

Generic job portals bury niche roles (e.g. Platform/DevOps engineering
roles centered on Infrastructure-as-Code) at specific companies you care
about under a flood of irrelevant postings. go-get-a-job instead watches
*exactly* the companies you tell it to, using each company's own public
job-board API, and uses an AI model to judge fit against a free-text
description of what you're looking for — not just keyword matching.

## How it works

```
 ┌─────────────┐   ┌────────────────┐   ┌───────────┐   ┌───────┐   ┌────────┐
 │ ATS sources  │──▶│ keyword filter │──▶│ AI scorer │──▶│ store │──▶│ notify │
 │ (per company)│   │ (cheap, free)  │   │(DeepSeek) │   │(SQLite)│  │ (ntfy) │
 └─────────────┘   └────────────────┘   └───────────┘   └───────┘   └────────┘
```

1. **Sources** (`internal/sources`) fetch every open posting from each
   configured company's ATS. Supported: Greenhouse, Lever, Ashby,
   SmartRecruiters, and a generic Workday connector (Workday has no
   official public API, but the request shape is the same for every
   Workday customer).
2. **Keyword filter** (`internal/filter`) cheaply skips obviously
   irrelevant postings (by title/description) before spending any AI
   budget on them.
3. **AI scorer** (`internal/filter`) sends surviving postings, plus your
   free-text profile, to an OpenAI-compatible chat completions API and
   gets back a 0-1 relevance score and a short human-readable reason.
4. **Store** (`internal/store`, SQLite) remembers every job it has ever
   processed, so nothing is re-scored or re-notified on later runs.
5. **Notify** (`internal/notify`, ntfy) pushes a notification for anything
   scoring above your threshold.

Each source's fetch also feeds a per-source health record, so a board that
returns nothing for long enough produces one `ntfy` warning rather than silence
(see `guard` under "Tuning relevance"). The run exits non-zero if any source
errored, so a scheduler sees a partial failure too.

Everything is config/interface-driven: adding a company is a config-only
change for Greenhouse/Lever/Ashby/SmartRecruiters/Workday sources, the AI
"profile" is free text you can edit any time, and the notifier is a small
interface so other channels could be added later without touching the
pipeline.

## Repository layout

```
cmd/go-get-a-job/        entrypoint - one pass per invocation, then exits
internal/config/      YAML schema, loading, validation
internal/model/       shared Job type
internal/sources/     one connector per ATS + registry
internal/filter/      keyword pre-filter + AI relevance scorer
internal/store/       SQLite-backed dedup store
internal/notify/      notifier interface + ntfy implementation
internal/runner/      orchestrates sources -> filter -> store -> notify
internal/sanitize/    strips control characters from untrusted text
internal/httpbody/    bounded reads of untrusted response bodies
config/               example YAML config (config.example.yaml)
deploy/               EXAMPLE Kubernetes manifests (no Kustomize/Helm)
Dockerfile            multi-stage build -> distroless static image
```

They are all examples: the config and the manifests describe a generic **Go
developer** persona. Keep your real one in a repository you control - see
[Configuring what it watches](#configuring-what-it-watches).

## Local development

```shell
make test        # go test ./...
make test-race   # go test ./... -race
make vet         # go vet ./...
make lint        # golangci-lint (version pinned in the Makefile)
make vuln        # govulncheck
make build       # builds ./bin/go-get-a-job
make run         # builds, then runs against config/config.example.yaml
```

Three flags:

| Flag | Default | Meaning |
|---|---|---|
| `-config` | `config.yaml` | Path to the YAML config. |
| `-log-level` | `info` | `debug`, `info`, `warn`, or `error`. `debug` adds one line per filtered-out posting, including the entry that decided it. |
| `-validate` | `false` | Fetch every configured source once, report per-source health, and exit non-zero if any looks unhealthy. No AI calls, no database writes, no notifications. See "Verifying a board before you add it". |

The whole pipeline is covered by unit tests using fakes/`httptest` servers
(no real network calls in `go test ./...`), and every source connector was
additionally validated against the real live APIs during development. Note
that boards are not forever - a company can retire its public board (see
"Verifying a board before you add it"), which fails quietly unless you read
the run's log line. Only
`ai.baseURL` and `notify.ntfy.url` are configurable per-deployment; you can
point them at local fake servers to smoke-test the full binary without
spending real API credits - see the `Score`/`Notify` interfaces in
`internal/filter` and `internal/notify` if you want to do the same.

Before deploying, read [Security](#security) - it explains the reasoning
behind the manifests in `deploy/`, and the parts that fail open unless you
verify them.

## Deploying to Kubernetes

All manifests in `deploy/` are plain YAML (no Kustomize/Helm) and were
validated with `kubectl apply --dry-run=server` against a real cluster.
They assume a `longhorn` StorageClass is available (used for the SQLite
and ntfy PVCs - block storage, not a network filesystem, since SQLite
explicitly warns against NFS-style locking). Swap `storageClassName` in
`deploy/pvc.yaml` / `deploy/ntfy-pvc.yaml` if your cluster uses something
else.

**Copy `deploy/` into your own (private) GitOps repository first.** If Argo CD
or Flux points at this repo, every change here deploys to your cluster - while
*your* config lives in a repository you do not own. One owner for both is the
point.

The commands below apply the manifests directly, which is the quickest way to
try things out; they assume you are in a clone of this repo.

### 1. Image

`.github/workflows/docker-build.yml` builds and publishes the image to
`ghcr.io/erik-schuetze/go-get-a-job` automatically on every push to `main`
(and on `v*` tags). Release image tags follow semver and mirror the git tag
exactly, so tag `v0.2.0` publishes `v0.2.0` - the same string as the GitHub
release and the same string you copy into the manifest. (`main` and a
short-SHA tag are published alongside it for traceability, but both are
mutable by definition, so nothing that runs unattended may reference them.)

`deploy/cronjob.yaml` points at a **release tag plus a digest**, e.g.:

```yaml
image: ghcr.io/erik-schuetze/go-get-a-job:v0.2.0@sha256:d1fff42c...
imagePullPolicy: IfNotPresent
```

The digest is what makes it immutable: a scheduled run always executes
exactly the reviewed image, never "whatever is newest in the registry".
That matters because the CronJob runs unattended - an unpinned `:latest`
plus `imagePullPolicy: Always` means every run is a silent, unreviewed
upgrade with no diff and no rollback artifact.

To move to a new release, change the tag *and* the digest in
`deploy/cronjob.yaml` in one reviewed commit; the previous digest is your
rollback. Both values come from the same `docker buildx imagetools
inspect` / `docker manifest inspect` output:

```shell
docker buildx imagetools inspect ghcr.io/erik-schuetze/go-get-a-job:v0.2.0
# -> look for the top-level "Digest:" (the multi-arch manifest list)
```

> `v0.1.0` predates that convention and was published as `0.1.0`, so it is
> the one release whose image tag has no leading `v`. Every release from
> `v0.2.0` on is `vX.Y.Z`.

Nothing to do here at all unless you've forked this to your own GitHub
account, in which case update the image reference in `deploy/cronjob.yaml`
too - the workflow publishes to your own `ghcr.io/<you>/go-get-a-job`
automatically once you push.

### 2. Create the namespace, config, ntfy, and network policy

```shell
kubectl apply -f deploy/namespace.yaml
# The example config, so the CronJob has something to mount. Replace it with
# your own before relying on the results - see "Configuring what it watches".
kubectl apply -f deploy/configmap.example.yaml
kubectl apply -f deploy/ntfy-configmap.yaml
kubectl apply -f deploy/ntfy-pvc.yaml
kubectl apply -f deploy/ntfy-deployment.yaml
kubectl apply -f deploy/ntfy-service.yaml
kubectl apply -f deploy/pvc.yaml
# Last, once the workloads above are confirmed healthy (see the security
# section for why the ordering matters):
kubectl apply -f deploy/networkpolicy.yaml
```

`deploy/namespace.yaml` also carries a Pod Security Admission `restricted`
label, and `deploy/networkpolicy.yaml` is a default-deny. Both are
fail-open when they don't engage, so **verify them** rather than trusting
that the YAML applied - see [Security](#security).

### 3. One-time ntfy user/token setup

The ntfy server starts with `auth-default-access: deny-all`, so nothing
can publish or subscribe until you create a user and grant it access to
one topic.

Create the pipeline user **without** `--role=admin` and grant it read/write
on the topic it actually publishes to:

```shell
kubectl -n go-get-a-job exec deploy/ntfy -- env NTFY_PASSWORD='<choose-a-password>' \
  ntfy user add go-get-a-job-user

# Grant access to the one topic the pipeline publishes to (default: job-matches).
kubectl -n go-get-a-job exec deploy/ntfy -- ntfy access go-get-a-job-user job-matches rw

kubectl -n go-get-a-job exec deploy/ntfy -- ntfy token add go-get-a-job-user
# -> prints something like: token tk_xxxxxxxxxxxxxxxxxxxx created for user go-get-a-job-user
```

Use that token as `NTFY_TOKEN` in the next step.

> **Why not `--role=admin`?** An admin token can administer the whole
> server - create users, read every topic, change settings. This server is
> commonly published under a public hostname so a phone can reach it away
> from home (see step 6), so the credential the pipeline carries should be
> the smallest one that works: publish/subscribe on one topic. `deny-all`
> plus a per-topic ACL means a leaked pipeline token can write to
> `job-matches` and nothing else.
>
> **Rotating it.** Treat this token as a real production credential:
> `ntfy token add` a new one, update the Secret, then
> `ntfy token remove <old-token>` (or delete the user, which revokes all of
> its tokens at once and forces a fresh login in the phone app as well).
> Everything the App uses is in `auth.db` on the ntfy PVC, so this is an
> `auth.db`-backed manual operation - nothing here is in Git.

To receive notifications yourself, create a second user for your phone and
give it `ro` (or `rw`) on the same topic:

```shell
kubectl -n go-get-a-job exec deploy/ntfy -- env NTFY_PASSWORD='<your-password>' \
  ntfy user add your-phone-user
kubectl -n go-get-a-job exec deploy/ntfy -- ntfy access your-phone-user job-matches ro
```

### 4. Create the secret

Create the Secret directly with `kubectl` - no file is ever written to disk,
so nothing sensitive passes through this repo, git, or any AI tool:

```shell
kubectl create secret generic go-get-a-job-secrets \
  --namespace go-get-a-job \
  --from-literal=DEEPSEEK_API_KEY='<your-deepseek-api-key>' \
  --from-literal=NTFY_TOKEN='<the-token-from-step-3>'
```

### 5. Deploy the CronJob

```shell
kubectl apply -f deploy/cronjob.yaml
```

By default it runs daily at 06:00 cluster time (`spec.schedule` in
`deploy/cronjob.yaml`) - jobs typically stay open for weeks, so there's no
need to poll more often unless you want tighter latency.

To test immediately rather than waiting for the schedule:

```shell
kubectl -n go-get-a-job create job --from=cronjob/go-get-a-job go-get-a-job-manual-1
kubectl -n go-get-a-job logs -f job/go-get-a-job-manual-1
```

### 6. Reach ntfy from your phone

In-cluster, ntfy is reachable at
`http://ntfy.go-get-a-job.svc.cluster.local` (see `deploy/ntfy-service.yaml`;
the Service keeps `port: 80` so callers never need a port suffix, while the
container itself listens on `8080` as an unprivileged user - `targetPort` is
spelled out explicitly for that reason).

For push notifications away from your home network, route a hostname to that
Service through whatever reverse-proxy + dynamic-DNS setup you already use for
your other personal sites, update `base-url` in `deploy/ntfy-configmap.yaml`
to match, then install the [ntfy app](https://ntfy.sh/#subscribe) and
subscribe to your topic (`job-matches` by default) using the username/password
from step 3.

**Be aware that this makes the ntfy server internet-facing.** Its own
`deny-all` auth is then the only thing between the open internet and your
notification store, which is why step 3 creates a least-privilege user and why
`deploy/ntfy-deployment.yaml` runs non-root on a pinned image. If your phone
can reach the cluster over a VPN or your LAN, prefer that over a public
hostname - nothing in this repo requires ntfy to be publicly reachable.

## Configuring what it watches

Every key lives in one YAML document. `config/config.example.yaml` is the
annotated reference for all of them, and `deploy/configmap.example.yaml` is the
same thing wrapped in a ConfigMap. Edit your own copy - no code changes or
rebuilds needed, just re-apply the ConfigMap.

A real config is not neutral: the `ai.profile` text reads like a short CV, and
`filter.locations` names the countries you can work from. Keep it in
a repository you control rather than a public one or a fork of one. Whatever
manages it, the ConfigMap is semi-trusted at run time - see [ConfigMap edits can
steal your secrets](#configmap-edits-can-steal-your-secrets).

### Adding a Greenhouse / Lever / Ashby / SmartRecruiters company

Find the company's board token from their careers page URL and add an
entry:

```yaml
sources:
  - type: greenhouse        # or lever, ashby, smartrecruiters
    company: some-board-token
    displayName: "Some Company"
```

### Adding a Workday company

Workday has no single well-known API host, so a one-time manual lookup is
needed:

1. Open the company's real careers page with browser devtools open
   (Network tab).
2. Search for any job title.
3. Find the request to
   `https://{host}/wday/cxs/{tenant}/{site}/jobs`.

```yaml
sources:
  - type: workday
    host: acme.wd3.myworkdayjobs.com   # full hostname from the URL
    tenant: acme                       # path segment after /wday/cxs/
    site: AcmeCareers                  # path segment after that (case-sensitive)
    displayName: "Acme"
```

### Verifying a board before you add it

A wrong or retired board token does **not** stop the run: the connector logs
one `ERROR source fetch failed` line, the other sources still produce
matches, and - since this release - the process exits `1` so a scheduler can
see that something went wrong. A misconfigured company can still sit in your
config for months producing nothing that you would notice, though, so check a
board before trusting it.

The quickest way is the binary itself, which is the only check that exercises
the exact connectors you will run:

```sh
make build
./bin/go-get-a-job -config myconfig.yaml -validate
```

It fetches every source in config order, one line each, and prints
`unhealthy: fetch failed` / `unhealthy: board returned no postings` /
`unhealthy: postings are missing required fields`. It makes the same requests
a real run would (paced by the shared client, so it cannot arrive as a burst),
and sends no notifications and no AI calls, so it costs nothing but the
requests. A **non-empty board that legitimately has no open roles** is
reported as unhealthy too - it cannot tell that case apart from a wrong slug,
which is exactly the ambiguity it is meant to surface.

To check one board by hand instead, one `curl` per company is enough:

```sh
# Greenhouse (200 = ok, 404 = board retired/renamed)
curl -sS -o /dev/null -w '%{http_code}\n' \
  https://boards-api.greenhouse.io/v1/boards/grafanalabs/jobs

# Lever / Ashby
curl -sS -o /dev/null -w '%{http_code}\n' https://api.lever.co/v0/postings/palantir?mode=json
curl -sS -o /dev/null -w '%{http_code}\n' https://api.ashbyhq.com/posting-api/job-board/openai

# SmartRecruiters is the exception: it answers 200 with an empty result for
# *any* slug (its slugs are also case-sensitive - "BoschGroup" resolves,
# "Bosch" does not), so check the body instead of the status code
curl -sS 'https://api.smartrecruiters.com/v1/companies/BoschGroup/postings' |\
  grep -o '"totalFound":[0-9]*'
```

Workday reports the specific problem in the status code, which is worth
knowing because only one of the four is fixable by editing your config:

```sh
curl -sS -o /dev/null -w '%{http_code}\n' -X POST \
  -H 'Content-Type: application/json' -H 'Accept: application/json' \
  -d '{"appliedFacets":{},"limit":20,"offset":0,"searchText":""}' \
  https://nvidia.wd5.myworkdayjobs.com/wday/cxs/nvidia/NVIDIAExternalCareerSite/jobs
```

- `200` - good tenant and site; use it.
- `422` - the **tenant** (host prefix) is wrong.
- `404` - tenant is right, the **site** slug is wrong (case-sensitive).
- `401` - the tenant exists but requires credentials, so anonymous API
  reads are off. No connector setting can work around this; skip the
  company.

Two companies that look like they should work but do not, so you don't
re-add them:

- **HashiCorp** - its Greenhouse board was retired (the old
  `boards.greenhouse.io/hashicorp` URL now redirects to a 404) around the
  IBM acquisition; hiring moved to IBM's careers site, which exposes no
  public JSON API.
- **Atlassian** - a real Workday customer on `atlassian.wd5.myworkdayjobs.com`,
  but every anonymous `/wday/cxs/...` request returns `401 Unable to verify
  credentials for system account`, for every site slug. Not scrapeable.

### Tuning relevance

Two knobs, with a clear division of labour: `filter` decides what is worth
asking about, `ai.profile` decides how much you want it.

- `filter.keywords`: the **recall** gate. A posting's title+description must
  match at least one entry, case-insensitively, before it is ever sent to the
  AI scorer. Keep it broad and list every adjacent title you would consider
  (`platform engineer`, `sre`, `devops`) - it exists to skip obviously
  irrelevant postings cheaply, not to rank them. Leave it empty (`[]`) to
  disable it.
- `filter.locations`: the **legal** gate - where you can actually work. A
  whitelist, not an allow/deny pair; see below.
- `filter.minAIScore`: 0-1 threshold for a notification to fire.
- `ai.profile`: the **precision** lever. Free text describing what you're
  looking for, and what the model actually judges postings against. This is
  where you say which of your keywords you want most ("platform engineering
  primarily, SRE for extra coverage"); the scoring prompt already discounts
  generic matches, so there is no ranked-keyword syntax to learn.
- `ai.instructions`: optional hard rules appended to the scoring prompt - see
  below.

#### `filter.locations` - accept, unmatched

A whitelist of the places you can legally work from. Naming one is what sends a
posting to the AI scorer; a posting that names any other place is dropped before
it costs an AI call. There is no `deny` list, so nothing needs updating when you
see a posting from a country you forgot to enumerate.

Matching is case-insensitive and **word-boundary aware**, and entries are
phrases. That means `US` matches `Austin, US` but not `Australia`, `Belarus`, or
`Prussia`, and `European Union` is a single entry rather than three words
matching independently.

```yaml
filter:
  locations:
    accept:
      - Germany
      - EMEA
      - European Union
    unmatched: reject
```

- **`accept`** is country and region names only. A posting located
  `"Berlin, Germany (Remote)"` matches `Germany`, so remote roles that name a
  place you can work from need no special entry.
- **`unmatched`** decides what happens to a location that names a real place you
  have not accepted: `reject` (the default) drops it, `pass` sends it to the AI
  scorer too. `reject` is safe as a default because remote postings never land
  here.
- Leave `accept` empty (`[]`) to skip location filtering entirely.

Anything that says nothing about *which* country you would be in goes to the AI
scorer regardless of this setting: an empty location, a filler value like `N/A`,
and any remote phrasing - `Remote`, `Fully remote`, `Anywhere`, `Worldwide`,
`Home office`, `Work from home`, `Ortsunabhängig`. That is deliberate. The set
of phrasings a portal might use for "anywhere" is not enumerable, and a
pre-filter that guesses wrong silently drops the global remote roles you most
want. So the pre-filter only ever rules on places it can read, and the
relocation rules in `ai.instructions` below do the rest.

The cost is that a posting located `"Remote - Canada"` reaches the scorer, which
is one AI call spent where a hardcoded denial would have been free. With
`minAIScore` at its default that posting is scored low and not notified.

Dropped-location diagnostics appear at the end of each run and, per posting,
under `--log-level debug`, so you can see *why* something was filtered rather
than guessing.

#### `ai.instructions` - hard rules the pre-filter can't express

A free-text block appended to the scorer's **system** prompt, after the fixed
scoring contract. Use it for constraints that are a matter of judgment rather
than a place the whitelist can recognize:

```yaml
ai:
  instructions: |
    Treat "Remote - <country>" exactly like an onsite role in that country.
    A role that is remote globally, or workable from Germany, is fine. If a
    posting requires being legally based outside Germany or requires
    relocation, score it 0.2 or below.
```

Two things to know:

- **It is trusted operator text.** It goes into the system prompt, not into the
  fenced-off, untrusted job posting, so anything you paste there is obeyed as an
  instruction. Never copy text out of a job posting, an issue, or an email into
  it. The postings themselves are still neutralized and fenced, and the JSON
  output contract cannot be overridden from here.
- **It costs tokens on every call.** It is capped (4000 characters) and the
  config fails to load if you exceed it.

#### `guard` - noticing a board that has gone quiet

```yaml
guard:
  deadSourceRuns: 14        # consecutive empty runs before a warning
  minRequestIntervalMs: 250 # floor between outbound requests
  maxRequestsPerRun: 10000  # hard ceiling on requests in one run
```

The problem this solves is not "a company is not hiring". It is a board that
is *broken*: a slug renamed or migrated to another ATS, an API that changed
shape, a company that moved hiring behind a login. Several providers answer a
nonexistent board with `200` and an empty list, which is byte-for-byte what a
company with nothing open returns - so the run reports success and you learn
nothing. `deadSourceRuns` is the only signal available for that case: every
source that returns postings has its counter reset, every source that returns
nothing has it incremented, and crossing the threshold sends one `ntfy`
warning naming the source.

Watch out for the deliberate choices baked in here:

- **Only successful fetches are counted.** A source that *errors* is not
  recorded at all - it is already reported as a source error. Counting it
  would let one flaky network day push a healthy board toward a false alarm,
  and a warning that fires on false alarms is one you stop reading.
- **14 is high on purpose.** From a response alone, "the board is broken" and
  "the company has nothing open" are indistinguishable. A boutique
  infrastructure company with three roles legitimately has none for weeks, and
  a warning that fires during those weeks buries the case that matters. Daily
  runs mean a real board failure is reported within about two weeks.
- **The warning repeats every `deadSourceRuns` runs while the source stays
  silent** - not just once. A one-shot reminder gets swiped away and the
  source then never speaks again; a warning on *every* run trains you to
  ignore it. `0` is rejected rather than treated as "off", so unset and
  disabled can never be confused: to disable it, set a value it will never
  reach, e.g. `3650`.
- **A failure to record health is logged, never fatal.** The guard must not
  cost you the matches the run actually found.
- **Detection is per source, not per connector type.** `guard` and the
  warning messages use the source's `Label()` (e.g. `greenhouse/grafanalabs`),
  because six Greenhouse boards are six sources sharing one connector name and
  one shared history would let five healthy boards hide a sixth.
- **`minRequestIntervalMs` and `maxRequestsPerRun` are the same limits
  described under "Request etiquette"**, moved from compile-time constants
  into config. The interval is global rather than per source: a per-source
  budget would give a 50-company config 50× the intended ceiling, which is the
  opposite of pacing.

## Security

This is a self-hosted watcher that runs unattended in a home cluster, talks to
three kinds of untrusted third party, and holds two credentials. This section
is the reasoning behind the hardening in `deploy/`, `internal/`, and `.github/`
- written down because most of it is invisible from the manifests alone, and
because the decisions here look arbitrary until you know what they're for.

### Threat model in one screen

**Assets**

1. `DEEPSEEK_API_KEY` and `NTFY_TOKEN` (Secret `go-get-a-job-secrets`).
2. ntfy's `auth.db` (password hashes and access tokens) on the ntfy PVC.
3. The integrity of the notification channel - a forged "match" is a phishing
   vector aimed at your phone.
4. The cluster itself: a foothold in either pod must not become a foothold
   in the cluster or the LAN.

**What is trusted, and what is not**

| Input | Trust |
|---|---|
| Secret values (env only, never on disk) | trusted |
| Your ConfigMap (GitOps-managed) | semi-trusted - see "ConfigMap edits" below |
| Job postings from the ATS APIs (titles, descriptions, IDs, URLs) | **untrusted** - anyone can publish a job posting |
| The AI provider's response (`score`, `reason`, `signals`) | **untrusted** |
| Pod network | no longer assumed trusted - see the NetworkPolicy |

**Secrets never touch this repo.** `deploy/secret.example.yaml` was removed
deliberately; the Secret is created with `kubectl create secret` (step 4) so no
secret material is written to disk, committed to Git, or passed through any
tool. `internal/config` only ever logs an env-var *name*, never its value.

### ConfigMap edits can steal your secrets

`ai.baseURL`, `notify.ntfy.url`, and `sources[].host` all come from a
ConfigMap, while `DEEPSEEK_API_KEY` and `NTFY_TOKEN` are injected as env vars
into the same pod. Anyone who can `update` that ConfigMap - but who cannot read
the Secret - can point `ai.baseURL` at a host they control and receive the API
key in an `Authorization` header on the next scheduled run. This is the
single most realistic attack path against this deployment, and it is why:

- `internal/config` validates those fields at startup: `https://` only for
  remote hosts, no userinfo, no query string, no fragment, a bare-hostname
  regex for `sources[].host`, and a slug charset for company/board/tenant/site
  values. A typo fails the run loudly instead of silently sending a token
  somewhere new. The one exception is deliberate: plain `http://` is allowed
  for `*.svc`, `*.svc.cluster.local`, `*.cluster.local`, `localhost`, and
  loopback IPs, because the in-cluster ntfy hop is plain HTTP by design.
- `deploy/networkpolicy.yaml` bounds where an edited URL could actually reach
  (`443` only, no arbitrary ports).
- **You should lock down who can edit the ConfigMap.** With one operator this
  is mostly theoretical, but if more than one person (or a CI system) has
  namespace access, add standard RBAC so that writing the ConfigMap requires
  at least as much trust as reading the Secret:

  ```shell
  kubectl -n go-get-a-job get rolebindings,clusterrolebindings -o wide
  ```

  The practical rule: **anyone who can edit the ConfigMap can exfiltrate the
  secrets**, so treat the two permissions as equivalent when granting access.

Note the AI provider is a third party: `ai.profile` (your own text) and the
full job description are sent to it. That is a data-sharing decision, not a
vulnerability - but if the postings you watch contain anything you'd rather
not send to an external service, run without `ai:` configured (the keyword
filter still works) or point `ai.baseURL` at a self-hosted
OpenAI-compatible endpoint.

### NetworkPolicy

`deploy/networkpolicy.yaml` is a default-deny with four narrow allows: the
CronJob may reach DNS, `443` to the internet, and ntfy in-namespace; ntfy may
reach DNS and may be reached by the CronJob and by the reverse proxy in front
of it. **This is fail-open**: if the CNI doesn't enforce NetworkPolicy, the
objects apply cleanly and do nothing. Verify it engages, and treat a
trivially-passing negative test as a finding rather than a success:

```shell
# 1. Is a policy-capable CNI actually running?
kubectl get pods -A | grep -iE 'cilium|calico|flannel|kube-router'
#    (k3s ships kube-router's policy controller, so this should be non-empty)

# 2. Positive: the happy path still works.
kubectl -n go-get-a-job create job --from=cronjob/go-get-a-job manual-1
kubectl -n go-get-a-job logs -f job/manual-1

# 3. Negative: a pod in this namespace must NOT reach something unrelated.
kubectl -n go-get-a-job run netpol-probe --rm -it --restart=Never \
  --image=busybox:1.36 -- wget -T5 -qO- http://kubernetes.default.svc.cluster.local/healthz
#    Expect a timeout / connection failure. If it *succeeds*, the CNI is not
#    enforcing and you should not treat this file as a control.
```

Two ordering notes:

- **In-cluster ntfy stays on `port: 80`** while the container listens on
  `8080`, and both ports are allowed in the policy. That is deliberate: the
  Service's `targetPort` is spelled out rather than defaulted, and the extra
  port covers CNIs that evaluate policy before service DNAT. If your CNI
  evaluates egress *before* DNAT, you'll also need to add your service CIDR as
  an `ipBlock` (k3s default `10.43.0.0/16`) - the comment in the file has the
  command to confirm yours.
- **The reverse proxy is in a different namespace.** A same-namespace-only
  ingress rule breaks the public hostname while everything in this repo still
  looks healthy. The policy matches `kubernetes.io/metadata.name: web`; change
  that label if your proxy lives elsewhere.

FQDN-based egress allowlisting would be a real tightening here, but it needs
Cilium. The file notes where to swap it in if you run Cilium; without it,
egress is scoped by port, so a ConfigMap-edited `ai.baseURL` could still reach
an attacker's own HTTPS endpoint. That case is handled by the startup
validation above, not by the policy.

### Pod Security Admission

`deploy/namespace.yaml` sets `pod-security.kubernetes.io/enforce: restricted`
(plus `audit`/`warn`). Before that label existed the namespace had no policy at
all, meaning the default `privileged` applied and a root container would have
been admitted without comment - which is exactly what the bundled ntfy server
used to be.

Both pods in the namespace satisfy `restricted`: non-root with an explicit
UID/GID, all capabilities dropped, `allowPrivilegeEscalation: false`,
`RuntimeDefault` seccomp, read-only root filesystem. Verify the label actually
enforces (a typo'd label enforces nothing, silently):

```shell
kubectl -n go-get-a-job run psa-probe --restart=Never --image=busybox:1.36 \
  --overrides='{"spec":{"containers":[{"name":"psa-probe","image":"busybox:1.36","securityContext":{"privileged":true}}]}}' -- sleep 1
#    Expect: a Forbidden error mentioning "violates PodSecurity ... restricted".
#    If it is admitted, the label is not on this namespace.
```

**Apply order matters.** The PSA label must land *after* the hardened
workloads are confirmed running, or it rejects the still-root pod and the
namespace goes red on the next sync. If a future workload genuinely cannot meet
`restricted`, downgrade `enforce` to `baseline` and keep `warn`/`audit` on
`restricted` - that surfaces the drift on every sync without blocking it. The
label is a one-line revert.

### The ntfy hop is plain HTTP inside the cluster, HTTPS outside it

The CronJob publishes to `http://ntfy.go-get-a-job.svc.cluster.local`, so
`NTFY_TOKEN` crosses the pod network unencrypted. That is a deliberate
decision, not an oversight:

- The Service is `ClusterIP` and the traffic never leaves the cluster's pod
  network.
- The NetworkPolicy restricts who can be on the other end of that hop to the
  ntfy pod.
- Terminating TLS there would mean plumbing a certificate for an internal
  hostname, for a hop that is already both private and scoped.

The asymmetry is real and worth stating: **the external hop (your phone, or
Caddy in front of ntfy) is HTTPS; the internal hop is not.** If you want the
internal hop encrypted too, serve ntfy with `listen-https` and point
`notify.ntfy.url` at `https://...` - the config validation already accepts it.

### Untrusted input handling

Defense in depth; none of these were exploitable bugs, and the reviews that
preceded them found no injection, SSRF, or panic vectors.

- **Response bodies are capped** (`internal/httpbody`) on every ATS call, the
  AI call, and ntfy error reads. Timeouts bound duration, not memory.
- **URLs are built with escaping, not `fmt.Sprintf`** (`internal/sources`), so
  a hostile board token or path can alter a path but never the origin, the
  query, or the scheme.
- **Text is sanitized before it reaches a log or an HTTP header**
  (`internal/sanitize`). A newline in a job title would otherwise forge extra
  structured-log records or break the notification; an ANSI/OSC escape would
  rewrite your terminal when you tail the logs by hand.
- **The ntfy `Click` header only accepts absolute `http`/`https`.** `Click` is
  a tap target in the phone app, and `Job.URL` comes straight from third-party
  data - a `javascript:` or `data:` URL would otherwise be offered to you as
  one.
- **Job postings are fenced in the LLM prompt.** Posting text is
  attacker-controlled (anyone can publish a posting), so it is wrapped in
  `<<<JOB_POSTING_BEGIN>>>` / `<<<JOB_POSTING_END>>>` markers, the system
  prompt states that only the system message carries instructions, and any
  fence-shaped text *inside* the posting is stripped so the boundary cannot be
  forged. `reason` length and `signals` count are capped after parsing, so a
  hostile response can't flood the log or the notification. Prompt injection
  here can at worst skew one score - the model has no tools and its output is
  only ever logged, stored, and sent to you.
- **The SQLite database is created `0600` in a `0700` directory**, and its
  schema is applied with a context so a cancelled run doesn't block.

### Supply chain

- **All GitHub Actions are pinned to full commit SHAs** with the version in a
  trailing comment, and every workflow sets an explicit least-privilege
  `permissions:` block. `softprops/action-gh-release` runs in a
  `contents: write` job, so a moved community tag there would be a
  write-capable token against this repo.
- **Both Dockerfile stages are pinned by digest**, as is the runtime ntfy
  image.
- **Dependabot** (`.github/dependabot.yml`) proposes weekly bumps for Go
  modules, the actions above, and Docker base images, grouped into single
  reviewable PRs. A bump shows up as a diff you can read instead of arriving
  silently at 06:00.
- **`security.yml`** runs `govulncheck` and `golangci-lint` on every push and
  PR, plus weekly against an unchanged tree (new vulnerabilities are published
  against code that already exists).

Run the same checks locally - the versions are pinned in the `Makefile` so CI
and your machine agree:

```shell
make lint       # golangci-lint, incl. gosec/bodyclose/errorlint/noctx
make vuln       # govulncheck
make test-race  # go test ./... -race
```

### Reporting a problem

This is a personal project with no security team behind it. If you find
something, open an issue - or if it's sensitive, use GitHub's private
vulnerability reporting on this repository rather than a public issue.

### Known gaps / follow-ups

Deliberately left out of this hardening pass, so they aren't lost:

- **Image signing.** Digests are pinned, but nothing verifies *who* built the
  image. Cosign + an admission policy (Kyverno/Connaisseur) would close that;
  both are cluster-wide concerns rather than something this repo can arrange
  on its own.
- **FQDN-based egress allowlisting.** Needs Cilium. Without it, egress is
  scoped by port only - see the NetworkPolicy section.
- **Encrypting the in-cluster ntfy hop.** A documented decision, not an
  oversight; see above.
- **Anything in front of ntfy.** If you publish it through a reverse proxy,
  that proxy is part of this deployment's attack surface and should be at
  least as hardened as these manifests (its own `securityContext`, a pinned
  image, TLS termination). It isn't managed here because it isn't this repo.
- **Narrowing ntfy's public exposure.** If ntfy is only ever reached from your
  phone over a VPN or LAN, restricting the public route to LAN source IPs - or
  dropping the public route entirely - removes a whole class of risk at
  essentially no cost to this app.

## Request etiquette

Every endpoint this tool reads belongs to someone else: the public job-board
APIs of companies you have no relationship with, and occasionally their
careers pages. Those endpoints are published for browsers, not for a polling
client, so the tool is a guest and behaves like one. These rules are
implemented once, in `internal/httpclient`, and every connector uses that
client - so they cannot be forgotten by a new connector added later:

- **It identifies itself.** Requests carry
  `User-Agent: go-get-a-job (+https://github.com/Erik-Schuetze/go-get-a-job)`.
  An operator who doesn't want to be polled can block it by name, or look up
  what is doing it, instead of having to guess. The value is a constant, not a
  config field, specifically so it can't be set to a browser's - pretending to
  be a browser is the behaviour that makes automated clients unwelcome.
- **It paces itself.** At most one outbound request every 250ms, counted
  globally rather than per host, so a run cannot arrive as a burst no matter
  how many boards it touches or how many goroutines a connector fans out with.
  The connector fan-out is real - a Workday or SmartRecruiters board needs one
  detail request per posting - and without a floor on the interval that fan-out
  is indistinguishable from a small flood.
- **It obeys a throttle instead of repeating it.** A `429` or `503` is retried
  up to twice, waiting as long as the response's `Retry-After` asked (capped at
  30s, so a daily run never stalls on an hour-long backoff). A `5xx` that
  isn't `503` is not retried: that usually means the request was wrong, and
  repeating it is noise rather than patience.
- **It cannot run away.** A run is capped at 10,000 outbound requests. That is
  not a rate limit but a bug-catcher: a pagination loop is the one failure mode
  that turns a polite client into a hostile one without anyone editing
  anything, and it should fail loudly instead of hammering.

Scraping an HTML careers page is not a problem in itself - some employers
publish jobs only that way - as long as it goes through this client, so a
scrape inherits the same identity, pacing, and backoff as an API call. What's
off the table is a client that fans out wide and fast enough to be mistaken for
an attack, which is what the pacing above exists to prevent.

One consequence worth knowing: pacing means a very large board takes a while.
At 250ms per request, a 200-posting Workday board needs roughly a minute. That
is the intended trade - this runs once a day, and being slow is cheap.

## Versioning and releases

This project follows [semver](https://semver.org), with one adaptation: **the
config schema is the public API**. That is what the version numbers speak to,
because the config is the thing you write and the thing that can break.

- **MAJOR** - incompatible changes. Removing or renaming a config key or CLI
  flag, or changing filter/scoring semantics in a way that invalidates a
  working config.
- **MINOR** - backwards-compatible additions: a new source connector, a new
  notifier, a new config key that has a default, a new optional flag.
- **PATCH** - bug fixes, documentation, and dependency bumps with no behavior
  change.

**The 0.x caveat applies right now.** While the major version is `0`, breaking
changes are released as MINOR bumps rather than MAJOR ones - so the
`filter.locations` whitelist rework in `v0.3.0` is a MINOR bump, not a MAJOR
one. The
config API is promoted to `v1.0.0` only as an explicit stability commitment,
not as a side effect of a feature landing.

Conventions the releases follow:

- The **git tag is the single source of truth**. The image tag mirrors it
  exactly, and the release notes come from the tagged commit (see
  `.github/workflows/`).
- The deployed `CronJob` pins the image by **tag and digest**, because a tag can
  be moved to point at different bytes. Repinning is a deliberate, reviewable
  commit.
- Every behavior change gets a `CHANGELOG.md` entry under Added / Changed /
  Fixed / Removed / Breaking.

## Not covered (by design, for now)

- **Google, SAP, Apple**: none expose a simple public job-search API
  (Google and Apple run fully custom career platforms; SAP's public job
  search has no open JSON endpoint) - adding them would mean bespoke, more
  fragile HTML scraping. Left out to keep the connector set reliable;
  revisit if it becomes worth the maintenance cost.
- **HashiCorp, Atlassian**: not a missing connector but unavailable data -
  HashiCorp's public Greenhouse board was retired (hiring now runs through
  IBM's careers site) and Atlassian's Workday tenant rejects anonymous API
  reads with `401`. Both were in the default config and removed; see
  "Verifying a board before you add it" for the details.
- **Discovery mode**: finding companies you haven't explicitly listed
  (e.g. via a self-hosted metasearch engine) was considered but is out of
  scope for now - this project only watches companies you configure.
- **Frontend**: matches arrive via ntfy; there's no web UI. The SQLite
  store (`store.path`) can be inspected directly with any SQLite client if
  you want to see history.

## License

[MIT](LICENSE)
