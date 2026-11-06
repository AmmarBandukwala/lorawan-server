FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN go test ./... && CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/fieldnode-api ./cmd/api

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/fieldnode-api /fieldnode-api
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/fieldnode-api"]
