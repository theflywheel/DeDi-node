# The build stage runs on the BUILDER's architecture, always, and Go
# cross-compiles to the target. Without --platform, buildx runs this entire
# stage under QEMU for every non-native target — emulating the Go compiler,
# which is the slowest thing in the build by an order of magnitude.
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Supplied by buildx per target platform.
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -o /dedid ./cmd/dedid

# Only this stage is per-target, and it does nothing but copy a binary in — so
# there is no compiler for QEMU to emulate and the arm64 image costs seconds
# rather than minutes.
FROM alpine:3.20
COPY --from=build /dedid /usr/local/bin/dedid
EXPOSE 8080
ENTRYPOINT ["dedid"]
CMD ["serve"]
