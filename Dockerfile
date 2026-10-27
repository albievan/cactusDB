# cactusdb in a container: the standalone web app listening on every address.
# Built by deploy-container.sh, which puts the Linux binary for the target
# architecture next to this file as ./cactusdb. By hand:
#
#   mkdir /tmp/ctx && cp dist/cactusdb-linux-amd64 /tmp/ctx/cactusdb && cp Dockerfile /tmp/ctx/
#   docker build -t cactusdb /tmp/ctx
FROM alpine:3.20

# ca-certificates for TLS database connections, tzdata so TZ works in log times.
RUN apk add --no-cache ca-certificates tzdata \
 && mkdir -p /opt/cactusdb/conn /opt/cactusdb/export \
 && chmod 0777 /opt/cactusdb/conn /opt/cactusdb/export

COPY cactusdb /opt/cactusdb/cactusdb

# /opt/cactusdb/conn:   cactus.conn.db (saved connections and settings) and
#                       cactus.key (the key that encrypts the credentials)
# /opt/cactusdb/export: exported files, in json/ csv/ xlsx/ xml/ sql/ backup/ subfolders.
#   It is the default export folder because it sits next to the program, so a
#   folder chosen in the web UI (saved in cactus.conn.db) still takes effect.
VOLUME ["/opt/cactusdb/conn", "/opt/cactusdb/export"]
WORKDIR /opt/cactusdb
ENV CACTUSDB_PORT=8080 PATH=/opt/cactusdb:$PATH
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD wget -q -O /dev/null "http://127.0.0.1:${CACTUSDB_PORT}/api/info" || exit 1

# Foreground: the action log goes to `docker logs`.
ENTRYPOINT ["/bin/sh", "-c", "exec cactusdb --host 0.0.0.0 --port \"$CACTUSDB_PORT\" --conn-db /opt/cactusdb/conn/cactus.conn.db \"$@\"", "cactusdb"]
