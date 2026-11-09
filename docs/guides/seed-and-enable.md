# Seed and enable

From a published image to a live activation, in order.

## Image and seeding

The published image is `ghcr.io/eshu-hq/eshu-collector-confluence`,
always `linux/amd64`, running as user 65532 with no shell. The binary
can install itself into a component folder, which is how a startup
(init) container sets it up:

```bash
collector seed --dest /component [--image-ref ghcr.io/eshu-hq/eshu-collector-confluence@sha256:<digest>]
```

That writes `/component/collector` (mode 0755) and
`/component/manifest.yaml`. It leaves a file alone when the bytes and
mode already match, so it's safe to run on every start, and it
replaces a changed binary in one step. `--image-ref` (or
`COLLECTOR_IMAGE_REF`) stamps the exact deployed image reference into
the manifest, because an image can't contain its own digest; the repo
manifest keeps a placeholder digest. Anything that isn't a plain
`name@sha256:<64 hex>` reference is refused before any file is
written. Installing still needs the four grants and the `eshu-hq`
publisher allowed, and `eshu component enable` isn't repeat-safe, so
your setup should tolerate "already enabled."

## Then record grants and enable

The host refuses this component's facts without a producer grant for
each kind. The four requests are in
[grant.example.json](../../grant.example.json), tied to `dev.eshu.collector.confluence`
and its manifest version. Record them before you enable (see
[concepts](../concepts/README.md) for why grants exist), enable one
activation per scope from your copy of
[config.example.yaml](../../config.example.yaml), then let Eshu run a
first claim and check it with [operate](operate.md).

If you're replacing Eshu's built-in collector, do the identity checks
in the [configuration reference](../reference/configuration.md) first.
Getting those wrong re-keys stored data, and that's much easier to
prevent than to undo.
