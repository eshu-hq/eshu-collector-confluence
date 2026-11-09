# Architecture

## Place in Eshu

Eshu is the core system. This repo is one collector: a small helper
process that reads one Confluence space or one root page tree and returns facts. Eshu's
component host starts it per claim, sends one bounded
`collector-sdk/v1alpha1` request on stdin, and reads one result from
stdout (`collector --sdk-stdio`). The host validates and commits;
this program stores nothing and holds no database, graph, queue, or
lease handle. The picture is in
[diagrams](../diagrams/README.md#system-context), and the per-claim
flow is the [claim lifecycle](../diagrams/README.md#claim-lifecycle).

Behind the table sits component `dev.eshu.collector.confluence`,
version `0.1.0`, collector kind `documentation`. It declares support
for Eshu core `>=0.0.5 <0.2.0` and speaks `collector-sdk/v1alpha1`
over the process adapter.

## Components in this repo

- The binary under `cmd/` is the process adapter entry point. It also
  seeds itself into a component folder (`collector seed`).
- The collector packages at the repo root hold config parsing, the
  provider client, fact building, and failure classification.
- `manifest.yaml` is embedded into the binary and declares identity,
  the fact list, and the core range. It keeps an all-zero placeholder
  image digest on purpose; the deployed digest is stamped at seed
  time (see [seed and enable](../guides/seed-and-enable.md)).
- Boundary tests fail the build if this repo ever imports Eshu core
  internals or another collector.

## Stack

Go, the collector SDK, and the exact module pins are listed in the
[configuration reference](../reference/configuration.md) under its
versions section. `go.mod` is the version of record; please don't
substitute another SDK line.

## Boundaries

What it never does, and the deliberate limits, are covered in
[concepts](../concepts/README.md). Facts it can emit are in the
[facts reference](../reference/facts.md).
