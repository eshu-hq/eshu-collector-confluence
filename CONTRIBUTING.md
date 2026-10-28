# Contributing

`make ci` is the whole local contract: it runs the `lint`, `test`, and `race`
commands that CI runs (vet, whole-repo `golangci-lint`, gofumpt format check,
`go mod tidy` check, actionlint, the 500-line `wc -l` file cap, build, tests,
and race with the coverage floor). The floor is 90% of statements
(`COVERAGE_FLOOR` in `ci.yml` and the `Makefile`); it may only rise.

CI selects lanes by changed paths (see `ci.yml`): docs-only PRs run no lane,
fixture-only PRs run `test` only, shell-only PRs run `shell` only, and Go PRs
run `lint`, `test`, `race`, `boundary`, and `image` (the Dockerfile builds the
binary from Go sources and `go.mod`/`go.sum`, so a go-class change must also
validate the image build). The `ci-complete` aggregator is the only required
check; it passes when needed lanes passed or were legitimately skipped.

Lint uses `golangci-lint` v2.12.2 with the org set (`govet`, `staticcheck`,
`ineffassign`, `unused`, `errcheck`; formatter `gofumpt`) in `.golangci.yml`.
Lint runs on the whole repo with no baselines; an exclusion needs a reason and
a linked open issue on the same lines.
