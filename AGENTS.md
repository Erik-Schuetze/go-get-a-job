# AGENTS.md

Guidance for AI assistants and automated contributors working in this
repository. It is about how changes are written down, not what they do - the
README and the code cover the rest.

## The README stays a README

`README.md` is the front door. Someone reads it once, top to bottom, to decide
whether to use this project and then to get it working. Add a feature and
document the feature - do not let it accumulate warnings, caveats, and asides.

- Say what a thing does and how to use it. Put *why* it works that way - the bug
  it prevents, the trade-off, the alternative that was rejected - in the code
  comment, the commit message, or the `CHANGELOG.md` entry.
- A short caveat earns its place when it changes what the reader should do. If it
  only matters to someone modifying the code, it belongs next to the code.
- When a change makes an existing paragraph wrong, rewrite or delete that
  paragraph instead of appending a correction. Growing by accretion is the
  failure mode this rule exists to prevent.
- Material that is genuinely long - reasoning, threat models, audits - goes in
  its own document and gets one line in the README linking to it.

The test: the README answers "how do I use this?". Anything that answers "why is
it like this?" belongs somewhere else. A README that has become a changelog with
a quickstart buried in it is a regression, even when every individual addition
was correct.

## Where everything else goes

| Content | Home |
|---|---|
| What a key/flag/field does, how to turn it on | `README.md` |
| Why it works that way; the bug it prevents | the code, as a comment |
| What changed, and whether it is breaking | `CHANGELOG.md` |
| Work deliberately deferred, with the context to pick it up later | `backlog.md` |
| A large body of reasoning or a security argument | its own document, linked from the README |

## House style

- Plain ASCII hyphens, not em dashes. Sentence case headings. Match the prose
  around you rather than importing a different voice.
- Comments explain reasoning that is not obvious from the code, not what the
  next line does.
- Keep `CHANGELOG.md` entries factual and short. "Breaking" means a config or
  flag that used to work no longer does; say what changed, not how to migrate it.

## Before you say it works

Run `make test`, `make vet`, and `make lint` (and `make vuln` for dependency
changes) - or say plainly that you could not. Never describe a change as tested,
working, or verified on the strength of having read it carefully.

## Shared working tree

Several people or agents may be editing this repository at once. Do not revert,
rewrite, or "tidy up" changes you did not make, and do not assume a file is
yours to restructure because it looks inconsistent - check whether it is
work in progress first. Never commit secrets; `deploy/secret.yaml` and
`backlog.md` are local-only and gitignored on purpose.
