#!/usr/bin/env bash
# Resolves the immutable digest of a pushed collector image and prints the
# digest-pinned reference to give the seed init container.
#
# Usage: pin-digest.sh <image-ref>      (name:tag, or name@sha256:...)
#
# This does not edit manifest.yaml. The manifest is embedded in the image, so a
# digest written into it would name a different image than the one that carries
# it. The repository keeps the all-zero placeholder digest; `collector seed`
# writes the real reference into the seeded manifest when the deployer passes it
# as --image-ref or COLLECTOR_IMAGE_REF, and the release job sets
# RELEASE_MANIFEST_PINNED=1 only for a manifest whose digest is real.
set -euo pipefail

ref="${1:?usage: pin-digest.sh <image-ref>}"
case "$ref" in
  *@sha256:*) pinned="$ref" ;;
  *)
    digest="$(docker inspect --format='{{index .RepoDigests 0}}' "$ref")"
    pinned="$digest"
    ;;
esac

if ! [[ "$pinned" =~ ^[a-z0-9][a-z0-9./_-]*(:[A-Za-z0-9._-]+)?@sha256:[a-f0-9]{64}$ ]]; then
  echo "pin-digest: '$pinned' is not a digest-pinned reference (name@sha256:<64 hex>)" >&2
  exit 1
fi
if [[ "$pinned" == *@sha256:0000000000000000000000000000000000000000000000000000000000000000 ]]; then
  echo "pin-digest: refusing the all-zero placeholder digest" >&2
  exit 1
fi

echo "Give the seed init container this image reference:"
echo "  collector seed --dest <component-home> --image-ref ${pinned}"
echo "  (or set COLLECTOR_IMAGE_REF=${pinned})"
