# eshu-collector-confluence

Pre-release. Holds no producer authority until its reviewed cutover
(eshu-hq/eshu#8855).

A read-only Confluence collector for [Eshu](https://github.com/eshu-hq/eshu),
built as a component extension on the public collector SDK. One claim reads one
Confluence space, or one root page tree, as one snapshot generation and returns
source-neutral `documentation_*` facts. It runs through the Eshu
component-extension host on the `process` runner: the host sends one bounded
JSON request on stdin and reads one result from stdout. The collector holds no
Postgres, graph, queue, lease, or workflow handle.

## What it emits

| Fact kind | Schema | One fact per |
| --- | --- | --- |
| `documentation_source` | 1.0.0 | claim (the space or page tree) |
| `documentation_document` | 1.0.0 | page |
| `documentation_section` | 1.1.0 | page body |
| `documentation_link` | 1.0.0 | link in a page body |

These are Eshu core kinds that the Git documentation path also emits, so core
consumers are unchanged. A core kind needs a producer grant (see below).
Payloads are built with the published `sdk/go/factschema` encoders and keep the
partial ACL posture the in-tree collector reports: Confluence page restrictions
are not collected, so access stays `partial` and is never upgraded to allowed.

Stable keys match the in-tree collector byte for byte. `FactID` does not: the
host builds the envelope. Parity with the in-tree collector is proven against
recorded fixtures; see [testdata/README.md](testdata/README.md), which also
lists every intentional difference.

## Result states

| State | When | Failure class (bounded) |
| --- | --- | --- |
| `complete` | every page was read and the cursor walk ended | none |
| `partial` | pages were skipped (unreadable, over `max_page_bytes`, over the result budget) or the walk hit `max_total_pages` | none; `warning_count` counts them |
| `retryable` | HTTP 429 and 500/502/503/504, a transient transport error, or the claim ran out of time | `rate-limited`, `provider-unavailable`, `transport-error`, `claim-deadline-exceeded` |
| `terminal` | bad config or credentials, HTTP 401, an unreadable space, an undecodable or oversize response, a transport error that repeated to claim attempt 20 | `invalid-config`, `missing-credentials`, `auth-failed`, `permission-denied`, `invalid-response`, `response-too-large`, `connection-failed`, `untrusted-next-link`, `transport-persistent`, `invalid-claim`, `unsupported_option` |

The collector never emits `unchanged`. The host supplies no previous snapshot
identity across claims, so the collector cannot prove a snapshot unchanged.
Instead the result carries a `freshness_hint`, a digest of every emitted fact,
and the core store skips a snapshot whose hint equals the active generation's.
Each claim still reads the whole scope, as the in-tree collector does on each
poll.

Errors, statuses, and logs carry only a class, a status code, and counts. They
never carry a token, URL, page id, title, or provider body.

## Configuration

Copy [config.example.yaml](config.example.yaml). It is the component activation
file: `host` names the claim identity, `process` launches the binary, and
`config` is this collector's own configuration. Unknown keys are rejected so a
typo cannot silently drop a bound.

- Run one activation per scope. Set `host.scope.id` to the id the in-tree
  collector used (`doc-source:confluence:<lower-case host>:<space id>`) so
  documentation read truth continues across the cutover.
- Credentials are read from the worker's environment. The activation names the
  variables (`email_env` and `api_token_env`, or `bearer_token_env`); the
  defaults are `ESHU_CONFLUENCE_EMAIL`, `ESHU_CONFLUENCE_API_TOKEN`, and
  `ESHU_CONFLUENCE_BEARER_TOKEN`. A bearer token wins over basic auth.
- `attestation.allow_empty_successor` maps the in-tree
  `ESHU_CONFLUENCE_ALLOW_EMPTY_SUCCESSOR` option. Absent and `false` mean no
  attestation. `true` is **refused**: the claim ends terminal with failure class
  `unsupported_option`, before any provider request, because the published
  collector SDK cannot carry the attestation (see Known gaps).
- The host reads at most `process.stdout_limit_bytes` of stdout per claim
  (4 MiB by default). Raise it for a large space and keep
  `limits.max_result_bytes` below it; pages that do not fit are left out whole
  and the snapshot is `partial`.

## Producer grants

The component declares core-owned kinds, so the core must grant each kind
before the component installs. Grants bind to the manifest's collector kind
(`documentation`), not to one source scope, so one grant set covers every
activation. [grant.example.json](grant.example.json) lists the four grants;
record each with `eshu component grant dev.eshu.collector.confluence --version
0.1.0 --kind <kind> --schema-version <v> --scope documentation --expires-in
<duration>`.

## Image and seeding

`Dockerfile` builds one static, pure-Go binary onto a shell-less distroless
base (`ghcr.io/eshu-hq/eshu-collector-confluence`, runs as 65532). The image is
also the seed source for the component home:

```bash
collector seed --dest /component [--image-ref ghcr.io/eshu-hq/eshu-collector-confluence@sha256:<digest>]
```

`seed` writes `DEST/collector` (mode 0755) and `DEST/manifest.yaml`. It leaves a
file alone when its bytes and mode already match, so an init container can run
it on every start, and it replaces a changed binary atomically. The manifest in
the repository keeps a placeholder artifact digest, because an image cannot name
its own digest. Pass the digest-pinned reference the pod runs (`--image-ref`, or
`COLLECTOR_IMAGE_REF`) and the seeded manifest names it. `scripts/pin-digest.sh`
resolves a pushed tag to that reference. Installing the seeded manifest still
needs the four grants and the `eshu-hq` publisher on the allowlist; `eshu
component enable` is not idempotent, so an init container must tolerate "already
enabled".

## Known gaps

- `eshu component inspect`, `verify`, and `conform`, and the public SDK
  conformance harness, reject any manifest that declares core-owned kinds,
  because they load it without producer-grant context. Installation with
  grants works. `TestPublicHarnessCannotYetJudgeCoreKindManifests` pins this and
  `TestFixturesPassTheHostChecks` runs the checks the host runs at emission.
- SDK v0.2.0 has no `Generation.TrustedDeletion`; it landed after the tag. The
  collector does not hand-marshal that unpublished wire field, so a scope that
  needs an empty or sharply smaller snapshot to replace the current one cannot
  be served yet: with the option off, the core store's successor guard refuses
  such a snapshot, as it does for any collector. Removal condition: a collector
  SDK release that carries `TrustedDeletion`, then set it from the option and
  delete the refusal in `parseAttestation`.
- `internal/stableid` and `internal/httpx` are copies of in-tree helpers. Replace
  them with the published eshu-sdk helpers when eshu-hq/eshu#8851 lands.

## Develop

```bash
go build ./... && go vet ./...
go test -race -count=1 ./...
```

Tests use `httptest` and recorded fixtures only. No test reaches a real
Confluence site. `testdata/conformance/*.json` are generated; refresh them with
`UPDATE_CONFORMANCE_FIXTURES=1 go test -run TestConformanceFixturesAreCurrent .`.
