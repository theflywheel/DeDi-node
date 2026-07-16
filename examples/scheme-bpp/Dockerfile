FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod .
COPY main.go .
RUN CGO_ENABLED=0 go build -o /scheme-bpp .

FROM alpine:3.20
COPY --from=build /scheme-bpp /usr/local/bin/scheme-bpp
EXPOSE 3002
ENTRYPOINT ["scheme-bpp"]
