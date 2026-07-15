FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /dedid ./cmd/dedid

FROM alpine:3.20
COPY --from=build /dedid /usr/local/bin/dedid
EXPOSE 8080
ENTRYPOINT ["dedid"]
CMD ["serve"]
