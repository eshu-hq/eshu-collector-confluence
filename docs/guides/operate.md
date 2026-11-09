# Operate

What you'll actually see once claims are running, and what every
ending means. Run states (`complete`, `partial`, `retryable`,
`terminal`) are explained in [concepts](../concepts/README.md).

## Signals you can rely on

The host keeps the collector's stderr text only as a byte count, so
the useful signal is the status row in each result.

| Result | Fields you get |
| --- | --- |
| `complete` or `partial` | `fact_count`, `source_latency_ms`, and for partial runs `partial` plus `warning_count` |
| `retryable` or `terminal` | `failure_class`, `retry_after_seconds` (retryable only), and `source_latency_ms` |

On current Eshu `main`, the worker also records each result as
`eshu_dp_component_extension_*` metrics and one
`component extension result` log line, and it honours
`retry_after_seconds` (capped at one hour, never below the poll
interval). Until a release carries that recording, only the failure
class reaches the work item. The binary also writes
`collector health` and `collector detail` debug lines to stderr for
local runs (request counts and the exact config problem); the host
doesn't surface them. This collector emits no metrics or traces of its
own: the manifest declares `eshu_dp_confluence_collector_`, but
nothing emits under it.

## Every way a claim can end

A failure class is a short, fixed label for what went wrong. The table
below is the full set from `contract.go`, with the mapping checked
against `client.go` (status handling) and `collect.go` (how errors
become terminal or retryable results). If you add a class in code,
add its row here in the same change.

| State | When it happens | Failure class |
| --- | --- | --- |
| `complete` | Every page was read and the page walk finished | none |
| `partial` | Some pages were skipped (unreadable, over the page-size cap, or over the result cap), or the walk hit the page-count cap | none; `warning_count` counts the skips |
| `retryable` | Rate limit (HTTP 429) | `rate-limited` |
| `retryable` | Retryable server error (500, 502, 503, or 504) | `provider-unavailable` |
| `retryable` | A temporary, transient network error | `transport-error` |
| `retryable` | The claim ran out of time, or its context was cancelled | `claim-deadline-exceeded` |
| `terminal` | The activation config is rejected | `invalid-config` |
| `terminal` | Credentials are absent or incomplete | `missing-credentials` |
| `terminal` | Confluence refused the credential (HTTP 401) | `auth-failed` |
| `terminal` | The credential can't read the space or page tree (HTTP 403 or 404) | `permission-denied` |
| `terminal` | Any other non-retryable provider status, the fallthrough in `client.go` status handling | `provider-rejected` |
| `terminal` | A response body can't be decoded as the expected JSON | `invalid-response` |
| `terminal` | A response is over the configured byte cap | `response-too-large` |
| `terminal` | A non-transient connection failure, such as TLS or DNS not found | `connection-failed` |
| `terminal` | A pagination link points outside the base URL | `untrusted-next-link` |
| `terminal` | The claim itself can't be served | `invalid-claim` |
| `terminal` | `attestation.allow_empty_successor: true`, which the pinned SDK can't carry yet | `unsupported_option` |
| `terminal` | Something failed inside the collector itself: a fact-build failure, a request that couldn't be built, or any error that isn't a provider error (`collect.go` maps those here) | `internal-error` |

Two of those rows are easy to miss, so they're worth spelling out.
`provider-rejected` is where every non-2xx status lands when it isn't
401, 403, 404, 429, or a retryable 5xx. `internal-error` is the
collector's own fault bucket, not Confluence's: if you see it, the
bug is on this side.

`retryable` means "try again later" and `terminal` means "stop until
a person fixes something." The host owns the retry budget: it releases
a retryable claim for another try until its attempt limit (10) is
spent, then sets the work aside (dead-letters it) and keeps the last
failure class. The collector sets no retry limit of its own.
