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
| `terminal` | bad config or credentials, HTTP 401, an unreadable space, an undecodable or oversize response | `invalid-config`, `missing-credentials`, `auth-failed`, `permission-denied`, `invalid-response`, `response-too-large`, `connection-failed`, `untrusted-next-link`, `invalid-claim`, `unsupported_option` |

The host owns the retry budget. A retryable result is released for another
attempt until the claim reaches the host's attempt limit (10 in
`collector-component-extension`), then the host dead-letters the work item and
keeps the class of the last failure. A transport failure that never clears
therefore ends as `transport-error` after 10 attempts; the collector sets no
limit of its own.

The collector never emits `unchanged`. The host supplies no previous snapshot
identity across claims, so the collector cannot prove a snapshot unchanged.
Instead the result carries a `freshness_hint`, a digest of every emitted fact,
and the core store skips a snapshot whose hint equals the active generation's.
Each claim still reads the whole scope, as the in-tree collector does on each
poll.

Errors, statuses, and logs carry only a class, a status code, and counts. They
never carry a token, URL, page id, title, or provider body.

## What operators see

The extension host buffers a collector process's stderr and records only its
size (`stderr_bytes`) in a launch failure. It is never logged or stored. The
collector reports its outcome as SDK status rows, one per claim, and each row
carries everything this collector reports:

| Row | Fields |
| --- | --- |
| complete or partial | `fact_count`, `source_latency_ms`, and for a partial snapshot `partial` and `warning_count` (skipped pages, plus one when the walk hit `max_total_pages`) |
| retryable or terminal | `failure_class`, `retry_after_seconds` (retryable), and `source_latency_ms` (how long the claim ran before it failed) |

The worker sets no status recorder yet, so the host drops these rows today and
only the failure class reaches the work item. The host records the rows once
eshu-hq/eshu#8908 lands; the latency, counts, and retry hint above are not
visible to operators before then.

`failure_class` is a bounded token from the table above. `unsupported_option`
means the activation set `attestation.allow_empty_successor: true`, the only
option the collector refuses (see Known gaps). `invalid-config` and
`missing-credentials` are rejected before any provider request.

The binary also writes a `collector health` JSON line and, for a failure, a
`collector detail` line to stderr (request counts by operation and outcome, the
exact config problem). They are a debug aid for running the binary by hand; the
host does not surface them. No metrics or traces are emitted (see Known gaps).

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
- The claim time budget is `limits.claim_timeout_seconds` (default 300) or the
  time the host gives the claim, whichever is smaller. The host sets the claim
  deadline to the **initial lease expiry** and kills the process there; the
  default lease TTL is 60 seconds (worker env
  `ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL`) and heartbeats do not extend the
  deadline. So a `claim_timeout_seconds` above the lease TTL has no effect. The
  collector stops starting work a margin before the deadline (5% of the time
  left, at most 2 seconds) and returns a `retryable` result with failure class
  `claim-deadline-exceeded` instead of being killed mid-write. Set the worker
  lease TTL to at least 5 minutes in the reference deployment for any
  activation whose space takes longer than about a minute to read; see the
  acceptance notes.
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
`COLLECTOR_IMAGE_REF`) and the seeded manifest names it; `seed` stamps only that
reference into `artifacts[].image` and refuses anything that is not a
`name[:port]/path@sha256:<64 hex>` reference (lower-case, no tag, no quote,
backslash, `$`, or whitespace; one trailing newline is ignored). A refused
reference exits non-zero and writes nothing. The release workflow
prints the digest reference to its job summary (see Releasing). The image is always `linux/amd64` (the manifest's platform),
whatever the builder is. Installing the seeded manifest still
needs the four grants and the `eshu-hq` publisher on the allowlist; `eshu
component enable` is not idempotent, so an init container must tolerate "already
enabled".

## Cutover acceptance notes

Check these on the reference activation before the in-tree collector is removed:

- **Space read time vs lease TTL.** Measure how long the largest space takes
  to read: the status row's `source_latency_ms` once the host records it
  (eshu-hq/eshu#8908), or by timing a run of the binary by hand against the
  space until then. It must fit inside the worker
  lease TTL minus the deadline margin. A read that does not fit retries with
  `claim-deadline-exceeded` until the host's attempt budget is spent, then
  dead-letters. Raise `ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL` on the worker
  to at least 5 minutes in the reference deployment before activating.
- **Live Confluence Cloud check.** Page URLs keep the in-tree rule (resolve
  `_links.webui` against `_links.base`). On Confluence Cloud that may drop the
  `/wiki` context path; this is inferred and unconfirmed. Check a few
  `canonical_uri` values against a live tenant. A fix is a follow-up after
  cutover (see [testdata/README.md](testdata/README.md)).
- **Scope id.** `host.scope.id` must equal the in-tree id so read truth
  continues.

## Releasing

Releases are tags. After the change merges, push a `v*` tag (for example
`v0.1.0`) on the merge commit. `.github/workflows/release.yml` runs only for a
pushed tag; it re-runs vet and the race tests, builds the `linux/amd64` image,
pushes `ghcr.io/eshu-hq/eshu-collector-confluence:<tag>`, writes the image
digest to the job summary, signs the digest with cosign (keyless), and attaches
a build provenance attestation and an SBOM attestation. It reads no secret but
`GITHUB_TOKEN`, deploys nothing, and publishes nothing for a pull request or a
branch push.

Before the first tag:

- **Restrict who can create `v*` tags.** A `v*` tag on any commit publishes,
  whether or not that commit is on `main`. Add a repository tag ruleset that
  limits creation of `v*` tags to maintainers.
- **Bump `metadata.version` in `manifest.yaml` before tagging.** The verify job
  fails unless the tag is `v` plus that version, optionally followed by a
  pre-release suffix after the first `-` (`v0.1.0-rc1`). A grant pins the
  manifest version, so the two must agree.

Then:

1. The first publish creates a private package. Make
   `ghcr.io/eshu-hq/eshu-collector-confluence` **public** in the package
   settings once; a worker cannot pull a private package without credentials.
2. Confirm an anonymous pull works after the visibility change. The workflow
   verifies with its own token, so it passes while the package is private:

   ```bash
   docker manifest inspect ghcr.io/eshu-hq/eshu-collector-confluence@sha256:<digest>
   ```

   Run it from a shell that is not logged in to GHCR (`docker logout ghcr.io`
   first if needed). It must print the manifest, not an authorization error.
3. Copy the digest reference from the job summary
   (`ghcr.io/eshu-hq/eshu-collector-confluence@sha256:<digest>`) into the
   activation's `seed.image`, and seed with `--image-ref` or
   `COLLECTOR_IMAGE_REF`.
4. Verify before use (the workflow ref is the tag that was pushed):

```bash
cosign verify ghcr.io/eshu-hq/eshu-collector-confluence@sha256:<digest> \
  --certificate-identity \
  https://github.com/eshu-hq/eshu-collector-confluence/.github/workflows/release.yml@refs/tags/v0.1.0 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://ghcr.io/eshu-hq/eshu-collector-confluence@sha256:<digest> \
  -R eshu-hq/eshu-collector-confluence
```

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
- No OTEL metrics or traces. Out-of-process collectors emit no metrics or
  traces by design (arbiter ruling, 2026-10-23): the SDK status record is the
  contract and the host records it once eshu-hq/eshu#8908 lands. The manifest
  declares the metric prefix `eshu_dp_confluence_collector_`, but nothing in
  this process emits it, and the in-tree Confluence metrics have no equivalent.
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
