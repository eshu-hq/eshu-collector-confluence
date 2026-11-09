# Release

## Build, test, release

```bash
go build ./... && go vet ./...
go test -race -count=1 ./...
make ci   # lint + test + race; the full local check CI runs
```

Tests use local test servers and recorded fixtures only, so no test
reaches a real Confluence site. Saved check files in
`testdata/conformance/*.json` are generated; refresh them with
`UPDATE_CONFORMANCE_FIXTURES=1 go test -run TestConformanceFixturesAreCurrent .`.

Releases run from `.github/workflows/release.yml` on every push to
`main` and on `v*` tags. Each run re-runs vet and race tests, builds
the `linux/amd64` image, writes the digest to the job summary, signs
it with cosign (keyless), and attaches provenance and SBOM
attestations. A `main` push publishes the moving `main`, `latest`, and
`sha-<full-sha>` tags. A version tag publishes that tag instead, and
only if the tag sits on the `main` head and matches
`metadata.version` in `manifest.yaml` (an optional pre-release suffix
such as `v0.1.0-rc1` is allowed). Nothing is published for a pull
request, and the workflow deploys nothing. Before the first tag, limit
`v*` tag creation to maintainers.

After a release, make the GHCR package public once (GitHub creates it
as private), confirm an anonymous pull works
(`docker manifest inspect ghcr.io/eshu-hq/eshu-collector-confluence@sha256:<digest>`
from a shell that isn't logged in), copy the digest reference from the
job summary into the deployment's `seed.image`, and verify before use:

```bash
cosign verify ghcr.io/eshu-hq/eshu-collector-confluence@sha256:<digest> --certificate-identity https://github.com/eshu-hq/eshu-collector-confluence/.github/workflows/release.yml@refs/tags/v0.1.0 --certificate-oidc-issuer https://token.actions.githubusercontent.com
gh attestation verify oci://ghcr.io/eshu-hq/eshu-collector-confluence@sha256:<digest> -R eshu-hq/eshu-collector-confluence
```
