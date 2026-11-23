# golang:1.25, not the 1.24 the ns module pins. The MCP Go SDK requires go 1.25.0 from
# v1.8.0 onward. The three modules are independent, so this line only covers mcp/: ns/
# keeps 1.24 because nothing it imports asks for more, and api/ carries its own 1.25
# pin in its Dockerfile now that the assistant imports the same SDK.
GO_IMAGE ?= golang:1.25

# The repository root, not this directory. Two tests read the network server's
# vocabulary file to catch this module's copy of it drifting, and mounting only
# mcp/ would make them fail rather than skip — a guard that cannot see what it
# guards must not be able to pass quietly.
ROOT ?= $(CURDIR)/..
WORKDIR_IN = /src/mcp

.PHONY: test vet fmt build run

test:
	docker run --rm -v "$(ROOT)":/src -w $(WORKDIR_IN) $(GO_IMAGE) go test ./...

vet:
	docker run --rm -v "$(ROOT)":/src -w $(WORKDIR_IN) $(GO_IMAGE) go vet ./...

fmt:
	docker run --rm -v "$(ROOT)":/src -w $(WORKDIR_IN) $(GO_IMAGE) gofmt -l -w .

build:
	docker run --rm -v "$(ROOT)":/src -w $(WORKDIR_IN) $(GO_IMAGE) \
		go build -trimpath -ldflags="-s -w" -o /out/fieldnode-mcp ./cmd/fieldnode-mcp

# run starts this server over stdio against a network server on the Docker host.
# It is interactive because stdio means the client owns the process: run it from
# an MCP host, or pipe initialize messages in by hand.
#
# NS_API_URL points at the application API (8091, token-protected) and
# NS_STATUS_URL at the health endpoint (8090, unauthenticated). They are separate
# because the network server keeps them on separate ports on purpose.
run:
	docker run --rm -i -v "$(ROOT)":/src -w $(WORKDIR_IN) $(GO_IMAGE) \
		-e NS_API_URL=http://host.docker.internal:8091 \
		-e NS_STATUS_URL=http://host.docker.internal:8090 \
		-e NS_API_TOKEN="$(NS_API_TOKEN)" \
		go run ./cmd/fieldnode-mcp
