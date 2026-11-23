# Build context is the REPOSITORY ROOT, not this directory:
#
#   docker build -f mcp/Dockerfile -t fieldnode-mcp .
#
# Because the drift guards read files that live outside this module, and they have
# to: two tests read the network server's vocabulary file to catch this module's copy
# of it drifting, and two read the console's tool list to catch the console offering
# a tool this server never registers. An image that could not see those files would
# have to skip those tests to build, which would make the guard pass in exactly the
# place nobody looks. So each guarded file is copied explicitly, by name, rather than
# copying the tree and hoping: if a guard gains a third file, the build fails loudly
# here instead of silently skipping.
FROM golang:1.25-alpine AS build
WORKDIR /src
# go.sum is copied with go.mod so the module cache step verifies checksums and
# only invalidates when a dependency actually changes.
COPY mcp/go.mod mcp/go.sum ./mcp/
RUN cd /src/mcp && go mod download
COPY ns ./ns
COPY src/features/network/constants.ts ./src/features/network/constants.ts
COPY mcp ./mcp
# Tests run during the build: an image that cannot pass its own suite is never
# produced. Nothing here needs a database or a network.
RUN cd /src/mcp && go test ./... && \
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fieldnode-mcp ./cmd/fieldnode-mcp

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fieldnode-mcp /fieldnode-mcp
# No EXPOSE, and no listening by default: this process serves MCP over stdio
# unless MCP_HTTP_ADDR names an address, in which case it listens and demands
# MCP_AUTH_TOKEN on every request. The compose service sets both.
#
# The binary is also its own health probe (`-healthz`), because there is no shell
# and no curl in this image for a healthcheck to run.
USER nonroot:nonroot
ENTRYPOINT ["/fieldnode-mcp"]
