FROM golang:1.24-alpine AS build
WORKDIR /src
# go.sum is copied with go.mod so the module cache step verifies checksums and
# only invalidates when a dependency actually changes.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Tests run during the build: an image that cannot pass its own suite is never
# produced. The pgstore integration test skips itself without NS_TEST_DSN.
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/ns ./cmd/ns

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/ns /ns
# 6858/udp is the packet-forwarder port gateways are pointed at; 8090 serves
# /healthz and /status.
EXPOSE 6858/udp 8090
USER nonroot:nonroot
ENTRYPOINT ["/ns"]
