# Facts reference: Confluence collector

This table mirrors [manifest.yaml](../../manifest.yaml). The manifest
is the source of truth, so if you change one you need to update the
other in the same change.

| Fact kind | Schema | Confidence | One fact is |
| --- | --- | --- | --- |
| `documentation_source` | `1.0.0` | `observed` | the space or page tree (one per claim) |
| `documentation_document` | `1.0.0` | `observed` | one page |
| `documentation_section` | `1.1.0` | `observed` | one page body |
| `documentation_link` | `1.0.0` | `observed` | one link in a page body |

Behind the table sits component `dev.eshu.collector.confluence`,
version `0.1.0`, collector kind `documentation`. It declares support
for Eshu core `>=0.0.5 <0.2.0` and speaks `collector-sdk/v1alpha1`
over the process adapter.

Every kind above belongs to Eshu core, so the host won't accept it
without a producer grant. The four requests live in
[grant.example.json](../../grant.example.json); see
[concepts](../concepts/README.md) for how grants and identity work,
and [seed and enable](../guides/seed-and-enable.md) for recording
them.
