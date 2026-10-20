# One image for the server, the relay and the admin CLI, with the web app.
#
#   docker build -t supermatrix .
#   docker run ... supermatrix                                  # the server
#   docker run ... --entrypoint supermatrix-relay supermatrix   # a relay
#
# The server listens on 8080 (HTTP: ACME challenges + redirects), 8443/tcp
# (HTTPS) and 8443/udp (QUIC), as a non-root user; publish them as 80, 443/tcp
# and 443/udp, and set SUPERMATRIX_QUIC_PUBLIC=<admin host>:443 so relays dial
# the published UDP port. Its state (database, certificates, cookie key) is in
# /data: mount a volume there.

FROM node:22-alpine AS web
WORKDIR /src/app
COPY app/package.json app/package-lock.json ./
COPY app/patches ./patches
RUN npm ci --no-audit --no-fund
COPY app/ ./
RUN npm run build:web

FROM golang:1.26-alpine AS go
WORKDIR /src
COPY server/go.mod server/go.sum ./
RUN go mod download
COPY server/ ./
RUN CGO_ENABLED=0 go build -buildvcs=false -trimpath -ldflags "-s -w" -o /out/ ./cmd/... \
 && mkdir -p /empty/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=go /out/ /usr/local/bin/
COPY --from=web /src/app/dist /app/web
COPY --from=go --chown=65532:65532 /empty/data /data
ENV SUPERMATRIX_DATA_DIR=/data \
    SUPERMATRIX_STATIC_DIR=/app/web \
    SUPERMATRIX_HTTP_ADDR=:8080 \
    SUPERMATRIX_HTTPS_ADDR=:8443
EXPOSE 8080/tcp 8443/tcp 8443/udp
VOLUME /data
ENTRYPOINT ["supermatrix-server"]
