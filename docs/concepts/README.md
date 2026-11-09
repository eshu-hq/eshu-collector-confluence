# Concepts: how the Confluence collector works

One run reads one Confluence space or one root page tree as a single
snapshot. Payloads go through the published `sdk/go/factschema`
encoders, and the same four kinds also come from Eshu's Git
documentation path, so readers of the data don't need to change when
you switch the source.

Access stays cautious on purpose. Confluence page restrictions aren't
collected, so access is reported as `partial` and is never upgraded to
allowed. Stored keys match the built-in collector byte for byte; the
fact ID doesn't, because the host builds the final envelope (the
wrapper Eshu stores). Recorded fixtures prove the match, and
`testdata/README.md` lists every deliberate difference if you want the
fine print.

Errors, statuses, and logs carry only a class, a status code, and
counts. They never carry a token, web address, page ID, title, or any
page content.

## Four kinds of fact

The full table lives in the [facts reference](../reference/facts.md) and mirrors `manifest.yaml`. The manifest wins if the two ever
disagree, so update them together in the same change. All four carry
confidence `observed`, meaning Eshu saw the source directly.


Behind the table sits component `dev.eshu.collector.confluence`,
version `0.1.0`, collector kind `documentation`. It declares support
for Eshu core `>=0.0.5 <0.2.0` and speaks `collector-sdk/v1alpha1`
over the process adapter.

## The shape of a run

The host sends one claim, the collector refuses bad config or missing
credentials before it touches Confluence, walks the whole scope,
builds one snapshot, and returns it for the host to validate and
commit. The pictures are the
[system context](../diagrams/README.md#system-context) and the
[claim lifecycle](../diagrams/README.md#claim-lifecycle).

A run ends as `complete`, `partial` (some pages were skipped and
counted as warnings), `retryable` (try again later), or `terminal`
(stop until a person fixes something). Each claim still reads the
whole scope, even if nothing changed; the result carries a freshness
hint (a digest of every fact returned) and Eshu skips storing a
snapshot whose hint matches the current one. It never returns
`unchanged`, because the host sends no earlier snapshot to compare
with. Every failure class behind `retryable` and `terminal` is
listed in [operate](../guides/operate.md).

## Identity when you switch, and grants

Set `host.scope.id` to the ID the built-in collector used,
`doc-source:confluence:<lower-case host>:<space id>`, so stored
documentation continues under the same identity after a switch. In
page-tree mode that ID carries the space ID of the first page read.
The config reference has the details.

All four kinds belong to Eshu core, so core has to grant each kind
before the component installs. A grant is written permission to
publish that kind. Grants are tied to the collector kind
(`documentation`), not to one space, so one grant set covers every
setup you run. `grant.example.json` lists the four, and you record
each with `eshu component grant dev.eshu.collector.confluence --version 0.1.0 --kind <kind> --schema-version <v> --scope documentation --expires-in <duration>`.

## Honest limits worth knowing upfront

An empty or much smaller snapshot can't yet replace the current one on
request. Setting `attestation.allow_empty_successor: true` ends the
claim as `terminal` with `unsupported_option`, before any Confluence
request, because the published SDK can't carry that promise yet. With
the setting off, Eshu's safety check refuses such a snapshot, as it
does for any collector. This should lift when an SDK release carries
the needed field.

`eshu component inspect` and `verify` judge this manifest once the
four grants are recorded. `eshu component conform` still refuses these
core kinds in the published SDK harness, even with grants; tests here
(`TestFixturesPassTheHostChecks`) run the same checks the host runs
when facts are emitted, so you're not left guessing.

And a few firm lines: it never writes to Confluence and never stores
anything itself. It never imports Eshu core internals or another
collector (`boundary_test.go` fails the build if that changes), using
only the published SDK modules, the standard library,
`golang.org/x/net/html`, and YAML parsing. Tokens, page titles, page
IDs, and page content never land in errors, logs, or status messages.
Two helper folders (`internal/stableid`, `internal/httpx`) copy
built-in helpers a standalone repo can't import yet; they'll be
replaced when shared SDK helpers are published.

## Pictures

The diagrams for this page (system context and claim lifecycle) live
in one place: [diagrams](../diagrams/README.md#system-context) and
[the claim lifecycle](../diagrams/README.md#claim-lifecycle). Each
one has a short text version next to it.
