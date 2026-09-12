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
config/               example YAML config
deploy/               plain Kubernetes manifests (no Kustomize/Helm)
Dockerfile            multi-stage build -> distroless static image
```

## Local development

```shell
make test    # go test ./...
make vet     # go vet ./...
make build   # builds ./bin/go-get-a-job
make run     # builds, then runs against config/config.example.yaml
```

The whole pipeline is covered by unit tests using fakes/`httptest` servers
(no real network calls in `go test ./...`), and every source connector was
additionally validated against the real live APIs during development. Only
`ai.baseURL` and `notify.ntfy.url` are configurable per-deployment; you can
point them at local fake servers to smoke-test the full binary without
spending real API credits - see the `Score`/`Notify` interfaces in
`internal/filter` and `internal/notify` if you want to do the same.

## Deploying to Kubernetes

All manifests in `deploy/` are plain YAML (no Kustomize/Helm) and were
validated with `kubectl apply --dry-run=server` against a real cluster.
They assume a `longhorn` StorageClass is available (used for the SQLite
and ntfy PVCs - block storage, not a network filesystem, since SQLite
explicitly warns against NFS-style locking). Swap `storageClassName` in
`deploy/pvc.yaml` / `deploy/ntfy-pvc.yaml` if your cluster uses something
else.

### 1. Image

`.github/workflows/docker-build.yml` builds and publishes the image to
`ghcr.io/erik-schuetze/go-get-a-job` automatically on every push to `main`
(and on `v*` tags). `deploy/cronjob.yaml` already points at
`ghcr.io/erik-schuetze/go-get-a-job:latest` - nothing to do here unless
you've forked this to your own GitHub account, in which case update both
the image reference in `deploy/cronjob.yaml` and the workflow will publish
to your own `ghcr.io/<you>/go-get-a-job` automatically once you push.

### 2. Create the namespace, config, and ntfy stack

```shell
kubectl apply -f deploy/namespace.yaml
kubectl apply -f deploy/configmap.yaml
kubectl apply -f deploy/ntfy-configmap.yaml
kubectl apply -f deploy/ntfy-pvc.yaml
kubectl apply -f deploy/ntfy-deployment.yaml
kubectl apply -f deploy/ntfy-service.yaml
kubectl apply -f deploy/pvc.yaml
```

### 3. One-time ntfy user/token setup

The ntfy server starts with `auth-default-access: deny-all`, so nothing
can publish or subscribe until you create a user and an access token:

```shell
kubectl -n go-get-a-job exec deploy/ntfy -- env NTFY_PASSWORD='<choose-a-password>' \
  ntfy user add --role=admin go-get-a-job-user

kubectl -n go-get-a-job exec deploy/ntfy -- ntfy token add go-get-a-job-user
# -> prints something like: token tk_xxxxxxxxxxxxxxxxxxxx created for user go-get-a-job-user
```

Use that token as `NTFY_TOKEN` in the next step. Use the same
username/password to log in from the ntfy mobile/desktop app once you can
reach the server (see step 5).

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

In-cluster, ntfy is only reachable at
`http://ntfy.go-get-a-job.svc.cluster.local` (see `deploy/ntfy-service.yaml`).
For real push notifications away from your home network, route a hostname
to that Service through whatever reverse-proxy + dynamic-DNS setup you
already use for your other personal sites, update `base-url` in
`deploy/ntfy-configmap.yaml` to match, then install the
[ntfy app](https://ntfy.sh/#subscribe) and subscribe to your topic
(`job-matches` by default) using the username/password from step 3.

## Configuring what it watches

Edit `deploy/configmap.yaml` (or `config/config.example.yaml` for local
runs) - no code changes or rebuilds needed, just re-apply the ConfigMap.

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

### Tuning relevance

- `filter.keywords` / `filter.locations`: cheap pre-filter, OR-matched,
  case-insensitive. Leave either empty (`[]`) to disable that filter.
- `filter.minAIScore`: 0-1 threshold for a notification to fire.
- `ai.profile`: free text describing what you're looking for - this is
  what the model actually judges postings against, so this is the main
  lever for changing what counts as a match.

## Not covered (by design, for now)

- **Google, SAP, Apple**: none expose a simple public job-search API
  (Google and Apple run fully custom career platforms; SAP's public job
  search has no open JSON endpoint) - adding them would mean bespoke, more
  fragile HTML scraping. Left out to keep the connector set reliable;
  revisit if it becomes worth the maintenance cost.
- **Discovery mode**: finding companies you haven't explicitly listed
  (e.g. via a self-hosted metasearch engine) was considered but is out of
  scope for now - this project only watches companies you configure.
- **Frontend**: matches arrive via ntfy; there's no web UI. The SQLite
  store (`store.path`) can be inspected directly with any SQLite client if
  you want to see history.

## License

[MIT](LICENSE)
