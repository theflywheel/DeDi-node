FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod .
COPY *.go .
RUN CGO_ENABLED=0 go build -o /bapapp .

FROM alpine:3.20
COPY --from=build /bapapp /usr/local/bin/bapapp
ENTRYPOINT ["bapapp"]

# On Railway there is no compose `command:`, so the deployed flags live here.
# The adapter is reached over the private network by service name.
CMD ["-listen", ":8080", "-caller", "http://onix-bap.railway.internal:8081/bap/caller"]
