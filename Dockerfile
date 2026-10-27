# cactusdb in a container: the standalone web app listening on every address.
# Built by deploy.sh / deploy-container.sh from a context that holds, per
# architecture, rootfs-<arch>/opt/cactusdb/{cactusdb,conn/,export/}. There
# is no RUN step, so images for another architecture (e.g. amd64 on an arm64
# Mac) build without emulation. By hand:
#
#   mkdir -p ctx/rootfs-amd64/opt/cactusdb/conn ctx/rootfs-amd64/opt/cactusdb/export
#   chmod 0777 ctx/rootfs-amd64/opt/cactusdb/conn ctx/rootfs-amd64/opt/cactusdb/export
#   cp dist/cactusdb-linux-amd64 ctx/rootfs-amd64/opt/cactusdb/cactusdb && cp Dockerfile ctx/
#   docker build --platform linux/amd64 -t cactusdb ctx
#
# The alpine base brings the CA certificates (TLS database connections) and
# busybox wget (health check); time zones are compiled into cactusdb.
FROM alpine:3.20
ARG TARGETARCH
COPY rootfs-${TARGETARCH}/ /

# /opt/cactusdb/conn:   cactus.conn.db (saved connections and settings) and
#                       cactus.key (the key that encrypts the credentials)
# /opt/cactusdb/export: exported files, in json/ csv/ xlsx/ xml/ sql/ backup/
#   subfolders. It is the default export folder because it sits next to the
#   program, so a folder chosen in the web UI (saved in cactus.conn.db) still
#   takes effect.
VOLUME ["/opt/cactusdb/conn", "/opt/cactusdb/export"]
WORKDIR /opt/cactusdb
ENV CACTUSDB_PORT=8080 PATH=/opt/cactusdb:$PATH
EXPOSE 8080

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD wget -q -O /dev/null "http://127.0.0.1:${CACTUSDB_PORT}/api/info" || exit 1

# Foreground: the action log goes to `docker logs` / `kubectl logs`.
ENTRYPOINT ["/bin/sh", "-c", "exec cactusdb --host 0.0.0.0 --port \"$CACTUSDB_PORT\" --conn-db /opt/cactusdb/conn/cactus.conn.db \"$@\"", "cactusdb"]
