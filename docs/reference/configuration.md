# Configuration reference: Confluence collector

Start from the copyable [example config](../../config.example.yaml); component identity lives in the [manifest](../../manifest.yaml). For the facts these settings produce, see [facts](facts.md).

Copy `config.example.yaml` and enable one scope per setup, for
example:

```bash
eshu component enable dev.eshu.collector.confluence --instance confluence-eng --claims --config config.example.yaml
```

The file has three parts. `host` names the job identity Eshu plans,
`process` says how Eshu starts the binary, and `config` holds this
collector's own settings. Unknown keys are rejected, so a typo can't
silently drop a limit. And please never commit a real host, token, or
user; the example uses reserved `.invalid` names on purpose.

## Settings, one by one

| `config` key | What it does |
| --- | --- |
| `source.base_url` | Confluence web address, for example `https://confluence.example.invalid/wiki` |
| `source.space_id` or `source.root_page_id` | Read one whole space, or one page tree from a root page. Exactly one is required |
| `source.space_key` | Optional label used in page-tree mode |
| `source.page_limit` | Pages per request, 1 to 250 |
| `source.max_total_pages` | Most pages in one walk; hitting the cap is reported as `partial` |
| `credentials.*_env` | Names of environment variables that hold the login, see below |
| `attestation.allow_empty_successor` | Leave it `false`; `true` is refused for now (see the limits in how it works) |
| `limits.*` | Time and size caps for one claim, see below |

For identity, set `host.scope.id` to the ID the built-in collector
used: `doc-source:confluence:<lower-case host>:<space id>`. In
page-tree mode it carries the space ID of the first page read. Get
that right and stored documentation continues under the same identity
after you switch.

## How credentials work

Secrets never go in the config file. Under `config.credentials` you
name the environment variables the worker already has: `email_env`
plus `api_token_env` for a Cloud API token, or `bearer_token_env` on
its own. If you don't set them, the defaults from `config.go` are
`ESHU_CONFLUENCE_EMAIL`, `ESHU_CONFLUENCE_API_TOKEN`, and
`ESHU_CONFLUENCE_BEARER_TOKEN`. If a bearer token resolves, it wins
over email-plus-token login. Secret values never appear in facts,
logs, or status messages, and bad setup or missing credentials
(`invalid-config`, `missing-credentials`) are refused before any
request to Confluence, which saves you a confusing half-run.

## Time and size caps

The time bound for one claim is the smaller of
`limits.claim_timeout_seconds` (default 300) and the time the host
allows. The host sets the deadline to the first lease expiry and stops
the process there. The default lease is 60 seconds
(`ESHU_COMPONENT_COLLECTOR_CLAIM_LEASE_TTL` on the worker) and
heartbeat messages don't extend it, so a timeout above the lease has
no effect. The collector stops starting work shortly before the
deadline (5% of the time left, at most 2 seconds) and returns
`retryable` with `claim-deadline-exceeded` instead of being stopped
mid-write. For any space that takes over a minute to read, set the
worker lease to at least 5 minutes.

Other caps under `config.limits` are `fetch_concurrency` (page reads
at once, 1 to 16), `max_page_bytes` (a larger page is skipped and the
snapshot is `partial`), `max_result_bytes` (the whole result), and
`max_response_bytes` (one response). The host reads at most
`process.stdout_limit_bytes` per claim (4 MiB by default), so raise it
for a large space and keep `max_result_bytes` below it. Pages that
don't fit are left out whole, never cut in half.

## Pins, exactly as the build has them

Straight from `go.mod`, so please don't substitute another SDK line.
Both SDK modules are `github.com/eshu-hq/eshu/sdk/go/*` at `v0.2.0`,
which means they come from the Eshu core repo, not from `eshu-sdk`.

| Module | Version |
| --- | --- |
| Go | `1.26.10` |
| `github.com/eshu-hq/eshu/sdk/go/collector` | `v0.2.0` |
| `github.com/eshu-hq/eshu/sdk/go/factschema` | `v0.2.0` |
| `golang.org/x/net` | `v0.62.0` |
| `gopkg.in/yaml.v3` | `v3.0.1` |
