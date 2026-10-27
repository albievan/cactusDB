#!/usr/bin/env bash
# Deploy cactusdb as a container: the standalone web app listening on every
# address, with the port published and the export folder and the saved
# connections folder (cactus.conn.db + cactus.key) stored on the host.
#
#   ./deploy-container.sh                       # build the image and start on port 8080
#   ./deploy-container.sh up --port 8087 --export-dir /srv/cactusdb/export --conn-dir /srv/cactusdb/conn
#   ./deploy-container.sh logs | status | restart | down
#
# Works with docker or podman. Run ./deploy-container.sh --help for every option.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
APP=/opt/cactusdb # where the image keeps the program, conn/ and export/

# Defaults; each can also be set in the environment (e.g. CACTUSDB_PORT=8087).
PORT=${CACTUSDB_PORT:-8080}
BIND=${CACTUSDB_BIND:-0.0.0.0}
DATA_DIR=${CACTUSDB_DATA_DIR:-$HOME/cactusdb}
EXPORT_DIR=${CACTUSDB_EXPORT_DIR:-}
CONN_DIR=${CACTUSDB_CONN_DIR:-}
SQLITE_DIR=${CACTUSDB_SQLITE_DIR:-}
NAME=${CACTUSDB_CONTAINER:-cactusdb}
IMAGE=${CACTUSDB_IMAGE:-cactusdb}
TZ_NAME=${TZ:-}
RUNTIME=${CONTAINER_RUNTIME:-}
FROM_SOURCE=no

usage() {
  cat <<EOF
Usage: $(basename "$0") [up|down|restart|logs|status] [options]

Commands:
  up        build the image and (re)start the container (default)
  down      stop and remove the container (your data folders are kept)
  restart   restart the container
  logs      follow the container's action log
  status    show the container, its health and the URLs to open

Options:
  --port N           port to publish and listen on (default $PORT)
  --bind ADDR        host address to publish on (default $BIND = all addresses;
                     127.0.0.1 = this host only)
  --data-dir DIR     base folder on the host (default $DATA_DIR); holds
                     export/ and conn/ unless set separately below
  --export-dir DIR   host folder for exports (default <data-dir>/export)
  --conn-dir DIR     host folder for cactus.conn.db and cactus.key
                     (default <data-dir>/conn)
  --sqlite-dir DIR   optional host folder of SQLite databases, mounted
                     read-only at /sqlite (open them as /sqlite/<file>)
  --name NAME        container name (default $NAME)
  --image NAME       image name (default $IMAGE)
  --tz ZONE          time zone for log times, e.g. Europe/Amsterdam
  --runtime CMD      docker or podman (default: whichever is installed)
  --from-source      compile the binary with Go first instead of using dist/
  -h, --help         show this help

Environment:
  CACTUSDB_PASSPHRASE  if set, passed to the container to encrypt saved
                       credentials with a passphrase instead of cactus.key
EOF
}

die() { echo "deploy-container: $*" >&2; exit 1; }

CMD=up
case "${1:-}" in
  up|down|restart|logs|status) CMD=$1; shift ;;
esac
while [[ $# -gt 0 ]]; do
  case "$1" in
    --port)        PORT=${2:?--port needs a value}; shift 2 ;;
    --bind)        BIND=${2:?--bind needs a value}; shift 2 ;;
    --data-dir)    DATA_DIR=${2:?--data-dir needs a value}; shift 2 ;;
    --export-dir)  EXPORT_DIR=${2:?--export-dir needs a value}; shift 2 ;;
    --conn-dir)    CONN_DIR=${2:?--conn-dir needs a value}; shift 2 ;;
    --sqlite-dir)  SQLITE_DIR=${2:?--sqlite-dir needs a value}; shift 2 ;;
    --name)        NAME=${2:?--name needs a value}; shift 2 ;;
    --image)       IMAGE=${2:?--image needs a value}; shift 2 ;;
    --tz)          TZ_NAME=${2:?--tz needs a value}; shift 2 ;;
    --runtime)     RUNTIME=${2:?--runtime needs a value}; shift 2 ;;
    --from-source) FROM_SOURCE=yes; shift ;;
    -h|--help)     usage; exit 0 ;;
    *)             usage >&2; die "unknown option: $1" ;;
  esac
done

if ! [[ $PORT =~ ^[0-9]+$ ]] || (( PORT < 1 || PORT > 65535 )); then die "bad --port: $PORT"; fi
EXPORT_DIR=${EXPORT_DIR:-$DATA_DIR/export}
CONN_DIR=${CONN_DIR:-$DATA_DIR/conn}

if [[ -z $RUNTIME ]]; then
  if command -v docker >/dev/null 2>&1; then RUNTIME=docker
  elif command -v podman >/dev/null 2>&1; then RUNTIME=podman
  else die "neither docker nor podman is installed"; fi
fi
command -v "$RUNTIME" >/dev/null 2>&1 || die "$RUNTIME is not installed"
"$RUNTIME" info >/dev/null 2>&1 || die "$RUNTIME is installed but not running (start Docker Desktop / the docker service)"

exists() { "$RUNTIME" container inspect "$NAME" >/dev/null 2>&1; }

# urls prints where to open the web app: every IPv4 address of this host
# when published on all addresses.
urls() {
  local ips
  if [[ $BIND == 0.0.0.0 ]]; then
    echo "  open http://localhost:$PORT"
    if command -v ip >/dev/null 2>&1; then
      ips=$(ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1)
    elif command -v ifconfig >/dev/null 2>&1; then
      ips=$(ifconfig | awk '/inet / && $2 != "127.0.0.1" {print $2}')
    fi
  else
    ips=$BIND
  fi
  local ip
  # Leave out container bridge networks (docker0 and friends).
  for ip in $ips; do
    case $ip in 172.1[7-9].*|172.2[0-9].*|172.3[01].*|10.88.*) ;; *) echo "  open http://$ip:$PORT" ;; esac
  done
}

case $CMD in
  down)
    if exists; then
      KEPT=$("$RUNTIME" inspect -f '{{range .Mounts}}{{if eq .Type "bind"}}{{.Source}} {{end}}{{end}}' "$NAME")
      "$RUNTIME" rm -f "$NAME" >/dev/null && echo "Removed container $NAME (data kept in: ${KEPT% })"
    else echo "No container named $NAME"; fi
    exit 0 ;;
  restart)
    exists || die "no container named $NAME; run: $0 up"
    "$RUNTIME" restart "$NAME" >/dev/null && echo "Restarted $NAME"
    exit 0 ;;
  logs)
    exists || die "no container named $NAME"
    exec "$RUNTIME" logs -f "$NAME" ;;
  status)
    exists || die "no container named $NAME"
    "$RUNTIME" ps -a --filter "name=^${NAME}$" --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}'
    PORT=$("$RUNTIME" inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$NAME" | sed -n 's/^CACTUSDB_PORT=//p')
    urls
    exit 0 ;;
esac

# ---- up: build the image ------------------------------------------------
case $("$RUNTIME" info --format '{{.Architecture}}' 2>/dev/null || uname -m) in
  x86_64|amd64)  ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) die "unsupported architecture: $("$RUNTIME" info --format '{{.Architecture}}' 2>/dev/null || uname -m)" ;;
esac

CTX=$(mktemp -d)
trap 'rm -rf "$CTX"' EXIT
if [[ $FROM_SOURCE == yes ]]; then
  command -v go >/dev/null 2>&1 || die "--from-source needs Go installed"
  VERSION=$(sed -n 's/^var version = "\(.*\)"/\1/p' "$SCRIPT_DIR/main.go")
  echo "Compiling cactusdb $VERSION for linux/${ARCH}…"
  (cd "$SCRIPT_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=$ARCH go build -trimpath \
    -ldflags "-s -w -X main.version=$VERSION" -o "$CTX/cactusdb" .)
else
  BIN=$SCRIPT_DIR/dist/cactusdb-linux-$ARCH
  [[ -f $BIN ]] || die "$BIN not found: run ./build.sh first, or use --from-source"
  cp "$BIN" "$CTX/cactusdb"
fi
chmod 0755 "$CTX/cactusdb"
# Only the binary and the Dockerfile go into the build: nothing else from
# dist/ (such as a cactus.conn.db or cactus.key) can end up in the image.
cp "$SCRIPT_DIR/Dockerfile" "$CTX/Dockerfile"
echo "Building image $IMAGE (linux/$ARCH)…"
"$RUNTIME" build -q -t "$IMAGE" "$CTX" >/dev/null

# ---- up: data folders on the host ---------------------------------------
mkdir -p "$EXPORT_DIR" "$CONN_DIR"
EXPORT_DIR=$(cd "$EXPORT_DIR" && pwd)
CONN_DIR=$(cd "$CONN_DIR" && pwd)
chmod 0700 "$CONN_DIR" # credentials and their key live here
# On SELinux hosts (Fedora, RHEL) the folders must be labelled for the container.
Z=
if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then Z=,Z; fi
MOUNTS=(-v "$CONN_DIR:$APP/conn:rw$Z" -v "$EXPORT_DIR:$APP/export:rw$Z")
if [[ -n $SQLITE_DIR ]]; then
  [[ -d $SQLITE_DIR ]] || die "--sqlite-dir $SQLITE_DIR is not a folder"
  SQLITE_DIR=$(cd "$SQLITE_DIR" && pwd)
  MOUNTS+=(-v "$SQLITE_DIR:/sqlite:ro$Z")
fi

ENVS=(-e "CACTUSDB_PORT=$PORT")
[[ -n $TZ_NAME ]] && ENVS+=(-e "TZ=$TZ_NAME")
[[ -n ${CACTUSDB_PASSPHRASE:-} ]] && ENVS+=(-e CACTUSDB_PASSPHRASE)

# Run as the calling user so exports and the connection files on the host
# belong to you, not root. Rootless podman maps root to you already.
USER_OPT=()
if [[ $RUNTIME != podman ]]; then USER_OPT=(--user "$(id -u):$(id -g)"); fi

# ---- up: (re)start the container ----------------------------------------
if exists; then
  echo "Replacing the existing container ${NAME}…"
  "$RUNTIME" rm -f "$NAME" >/dev/null
fi
"$RUNTIME" run -d --name "$NAME" --restart unless-stopped \
  ${USER_OPT[@]+"${USER_OPT[@]}"} "${ENVS[@]}" "${MOUNTS[@]}" \
  -p "$BIND:$PORT:$PORT" "$IMAGE" >/dev/null

# Wait until it answers (or show why it didn't).
for _ in $(seq 1 30); do
  if [[ $("$RUNTIME" inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null) != true ]]; then
    "$RUNTIME" logs "$NAME" >&2 || true
    die "the container stopped; its log is above"
  fi
  if "$RUNTIME" exec "$NAME" wget -q -O /dev/null "http://127.0.0.1:$PORT/api/info" 2>/dev/null; then
    VERSION=$("$RUNTIME" exec "$NAME" cactusdb --version)
    echo "$VERSION is running in container $NAME"
    urls
    echo "  saved connections: $CONN_DIR/cactus.conn.db (key: $CONN_DIR/cactus.key)"
    echo "  exports:           $EXPORT_DIR/{json,csv,xlsx,xml,sql,backup}"
    [[ -n $SQLITE_DIR ]] && echo "  SQLite files:      $SQLITE_DIR, open them as /sqlite/<file> (read-only)"
    echo "  log:  $0 logs     stop: $0 down"
    exit 0
  fi
  sleep 1
done
"$RUNTIME" logs "$NAME" >&2 || true
die "cactusdb did not answer within 30 seconds; its log is above"
