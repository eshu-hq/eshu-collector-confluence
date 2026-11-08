# Contributing

Thanks for helping. The full contract lives in the root
[CONTRIBUTING.md](../../CONTRIBUTING.md); this page is the friendly
front door to it.

## Setup

You need Go at the version in `go.mod` and `golangci-lint` (the
Makefile can install its pinned version). Clone the repo and you're
set; tests run against fixtures and local test servers, never a
live provider.

## The local check

Run this before every push:

```bash
make ci
```

That's lint, test, and the race suite with a 90% coverage floor — the same commands CI runs. CI stays
authoritative; `make ci` is the whole local contract.

## Pull-request rules worth knowing

- No `.go` file over 500 lines. Split the file instead of asking
  for an exclusion; the lint lane enforces it.
- Lint runs on the whole repo with no baselines. Fix the finding.
- `manifest.yaml` keeps the all-zero placeholder image digest.
  A real digest in the repo would describe an older build than the
  image carrying it.
- If you change a fact kind, setting, or failure class, update
  `manifest.yaml` and these docs in the same change.
- Docs-only pull requests run no CI lane and stay mergeable through
  `ci-complete`.

More detail, lane by lane, is in
[CONTRIBUTING.md](../../CONTRIBUTING.md).
