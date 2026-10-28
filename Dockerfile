# check=skip=FromPlatformFlagConstDisallowed
# Eshu Confluence collector image: one static, pure-Go binary on a shell-less
# base, built reproducibly with -trimpath. It is a seed image as well as a
# runtime: an init container runs `collector seed --dest DIR` to copy the binary
# and manifest.yaml into a component home, then the Eshu CLI installs, enables,
# and grants the component. With no arguments it serves one claim from stdin.
#
# Build:  docker build -t ghcr.io/eshu-hq/eshu-collector-confluence:dev .
# The image is always linux/amd64, the platform the manifest artifact declares,
# whatever the builder is (the check directive above allows the constant
# platform on purpose): the build stage runs natively and cross-compiles, and
# the final stage is pinned to amd64 (both base digests are multi-arch indexes).
# Base digests resolved 2026-10-28; re-pin both when bumping either base.
FROM --platform=$BUILDPLATFORM golang:1.26.10-alpine@sha256:a2012b4a1d1f9f7e9f44e0d39b805a901e856b1dd80b4e6ee4338db547050308 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w' -o /out/collector ./cmd/collector

FROM --platform=linux/amd64 gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/collector /usr/local/bin/collector
USER 65532:65532
ENTRYPOINT ["/usr/local/bin/collector"]
CMD ["--sdk-stdio"]
