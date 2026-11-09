# Getting started

Status: pre-release. Please don't rely on this in production yet.

The honest first thing to know: you don't run this collector as a
daemon. Eshu's host starts it for one claim (one job) at a time, sends
one bounded request on stdin, and reads one result from stdout. So
"getting started" really means getting one activation enabled and one
claim to succeed.

1. Check your core version fits the range in the [manifest](../../manifest.yaml)
   (`>=0.0.5 <0.2.0`).
2. Copy [config.example.yaml](../../config.example.yaml) and enable one
   scope per setup:

   ```bash
   eshu component enable dev.eshu.collector.confluence --instance confluence-eng --claims --config config.example.yaml
   ```

3. Set `source.base_url` and exactly one of `source.space_id` /
   `source.root_page_id`. Credentials ride in environment variables
   (defaults `ESHU_CONFLUENCE_EMAIL`, `ESHU_CONFLUENCE_API_TOKEN`,
   `ESHU_CONFLUENCE_BEARER_TOKEN`). See the
   [configuration reference](../reference/configuration.md).
4. Record the four producer grants, then seed the image (see
   [seed and enable](../guides/seed-and-enable.md)). If you're switching
   from the built-in collector, set `host.scope.id` to
   `doc-source:confluence:<lower-case host>:<space id>`.
5. Let Eshu run a first claim. `complete` or `partial` with a
   `warning_count` is success. For any space that takes over a minute to
   read, set the worker lease to at least 5 minutes first.

Where to go next: [concepts](../concepts/README.md) for the mental
model, the [configuration reference](../reference/configuration.md)
for every setting, [facts](../reference/facts.md) for what comes out,
and [troubleshoot](../guides/troubleshoot.md) when the first claim
doesn't land.
