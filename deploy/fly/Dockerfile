# Fly builds from source; keep upstream's runtime-only Dockerfile unchanged.
FROM golang:1.27.0-alpine@sha256:4c9fe60190a2a3350ddc51de80d0224b8a6698d12bdfc999fee45ea9d6c46dbc AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/
ARG GARMIN_VERSION=fly-deploy
ARG GARMIN_COMMIT=unknown
RUN CGO_ENABLED=0 go build -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=${GARMIN_VERSION} -X main.commit=${GARMIN_COMMIT}" \
    -o /out/garmin-mcp ./cmd/garmin-mcp

# Fly mounts a root-owned volume. A short root entrypoint creates one nonroot
# directory on first boot, then execs the application as upstream's UID/GID.
FROM alpine:3.23@sha256:85fe1e81d6758c208f3e1eed4338a1997e19d4be002d4dd32d3100c9a8c010a0
RUN apk add --no-cache ca-certificates su-exec
COPY --from=build /out/garmin-mcp /usr/local/bin/garmin-mcp
COPY LICENSE THIRD_PARTY_NOTICES.md /licenses/garmin-mcp/
COPY --chmod=755 deploy/fly/entrypoint.sh /usr/local/bin/fly-entrypoint
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/fly-entrypoint"]
CMD ["serve", "--enable-write-tools=false", "--enable-destructive-tools=false"]
