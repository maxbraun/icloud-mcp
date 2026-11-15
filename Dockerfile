# syntax=docker/dockerfile:1
# Static, reproducible build. Keep this digest aligned with the release builder
# in the Makefile.
FROM golang:1.26.11@sha256:957952ae74dc010608717144399a59a2c6e82ea03d63be1c7ff6577c5a135f93 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .

ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
    -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/icloud-mcp ./cmd/icloud-mcp

# Distroless includes the CA bundle required for iCloud's TLS endpoints and
# has no shell or package manager at runtime.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

COPY --from=build /out/icloud-mcp /usr/local/bin/icloud-mcp
USER nonroot:nonroot
EXPOSE 8080 8081
ENTRYPOINT ["/usr/local/bin/icloud-mcp"]
