GO_IMAGE ?= golang:1.24

# DSN for the opt-in pgstore integration test. On Docker Desktop the
# host.docker.internal name resolves on its own; on Linux add
# --add-host=host.docker.internal:host-gateway to the test-pg recipe.
NS_TEST_DSN ?= postgres://postgres:ns@host.docker.internal:5433/ns?sslmode=disable

.PHONY: test test-short test-pg vet fmt demo smoke run

test:
	docker run --rm -v "$(CURDIR)":/src -w /src $(GO_IMAGE) go test ./...

# test-pg runs the store round trip against a real PostgreSQL, so the SQL in
# internal/pgstore is executed and not merely compiled. Without
# NS_TEST_DSN the suite skips itself, which is why it is a separate target.
test-pg:
	docker run --rm -v "$(CURDIR)":/src -w /src -e NS_TEST_DSN="$(NS_TEST_DSN)" \
		$(GO_IMAGE) go test ./internal/pgstore/ -count=1 -v

# test-short skips anything that needs a network, a socket held open for long,
# or a database; useful in a loop while editing.
test-short:
	docker run --rm -v "$(CURDIR)":/src -w /src $(GO_IMAGE) go test ./... -short -count=1

vet:
	docker run --rm -v "$(CURDIR)":/src -w /src $(GO_IMAGE) go vet ./...

fmt:
	docker run --rm -v "$(CURDIR)":/src -w /src $(GO_IMAGE) gofmt -l -w .

# demo narrates a whole OTAA session — join, uplink, downlink, ADR, dedup,
# replay — against the simulated device, with no radio attached.
demo:
	docker run --rm -v "$(CURDIR)":/src -w /src $(GO_IMAGE) go run ./cmd/nsdemo

# smoke runs the real ns binary and the real nsbench tool against each other
# over a real UDP socket, then checks /healthz, /status and SIGTERM handling.
smoke:
	docker run --rm -v "$(CURDIR)":/src -w /src $(GO_IMAGE) bash ./scripts/smoke.sh

# run starts the network server on the Docker host, in memory, with one seeded
# device so a gateway or nsbench can join immediately.
run:
	docker run --rm -it -p 127.0.0.1:6858:6858/udp -p 127.0.0.1:8090:8090 \
		-e NS_NET_ID=000000 \
		-e NS_SEED_DEVICES=0011223344556677:0102030405060708:2b7e151628aed2a6abf7158809cf4f3c \
		-e NS_LOG_LEVEL=debug $(GO_IMAGE) go run ./cmd/ns

