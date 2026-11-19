FROM golang:1.24-alpine AS build
WORKDIR /src
# go.sum has to come with go.mod: without it `go mod download` cannot verify
# checksums, and the layer is meant to be the verified dependency cache.
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fieldnode-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fieldnode-api /fieldnode-api
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/fieldnode-api"]
