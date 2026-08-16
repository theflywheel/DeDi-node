# The application behind our ONIX BPP adapter. Stdlib only, so no module fetch
# and no lockfile to drift.
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod .
COPY *.go .
RUN CGO_ENABLED=0 go build -o /flywheel-bpp .

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
COPY --from=build /flywheel-bpp /usr/local/bin/flywheel-bpp
# LISTEN, BPP_CALLER_URL and UPSTREAM_URL come from the environment; see main.go.
ENTRYPOINT ["flywheel-bpp"]
