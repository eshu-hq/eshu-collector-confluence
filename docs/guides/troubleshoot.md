# Troubleshoot

Start from the failure class on the work item; it's the main signal
the host passes through. The full class tables are in
[operate](operate.md).

## Before you switch a live space over

Two checks save trouble later. First, measure the largest space's read
time (the status row's `source_latency_ms`, or time a local run) and
make sure it fits inside the worker lease. Second, compare a few
stored page addresses (`canonical_uri`) against the live site. This
collector joins the base address and page path so Cloud pages keep
their `/wiki` prefix, which the built-in collector dropped, so the
first claim after a switch stores a fresh snapshot. That live-site
check is still open work, so don't rely on those addresses until
you've compared them yourself.

## Troubleshooting

Seeing `claim-deadline-exceeded` again and again usually means the
space takes longer than the lease, not that Confluence is down. Raise
the worker lease (see [configuration](../reference/configuration.md)) and watch
`source_latency_ms`. A steady `permission-denied` points at the
credential's access to that space, while `auth-failed` means the
credential itself was refused. If you only have a class and need the
detail, run the binary locally; the `collector detail` stderr line
spells out the exact config problem, and the host never shows it.
