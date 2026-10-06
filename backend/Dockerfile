# Build stage.
#
# modernc.org/sqlite is pure Go, so the whole binary builds with CGO disabled and
# runs on a minimal base image with no libc shims.
FROM golang:1.26-alpine AS build

WORKDIR /src

# Dependency layer: cached until go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Static binary; trim paths and symbols for a smaller image.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath -ldflags="-s -w" \
      -o /out/impactpay ./cmd/server

# Runtime stage.
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata wget && \
    adduser -D -u 10001 impactpay

WORKDIR /app

COPY --from=build /out/impactpay /usr/local/bin/impactpay

# Evidence storage and the SQLite fallback both live under /app/data.
RUN mkdir -p /app/data/storage && chown -R impactpay:impactpay /app

USER impactpay

ENV PORT=8080 \
    SQLITE_PATH=/app/data/impactpay.db \
    STORAGE_LOCAL_DIR=/app/data/storage \
    AUTO_MIGRATE=true

EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
  CMD wget -qO- "http://127.0.0.1:${PORT}/api/health" >/dev/null || exit 1

ENTRYPOINT ["impactpay"]
