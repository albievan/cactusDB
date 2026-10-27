#!/usr/bin/env bash
# Deploy cactusdb as a container: on Docker (dd), k3s or Kubernetes (k8s).
#
#   ./deploy.sh --docker dd  --env .env --install     # Docker / Docker Desktop / Podman
#   ./deploy.sh --docker k3s --env .env --install     # k3s
#   ./deploy.sh --docker k8s --env .env --install     # any Kubernetes cluster
#   ./deploy.sh --env .env --status
#   ./deploy.sh --env .env --uninstall [--purge]
#
# Every setting lives in the env file; .env.example lists them all with their
# defaults. cactusdb listens on every address, the port is published, and the
# export folder and the saved-connections folder (cactus.conn.db + cactus.key)
# are kept outside the container. Run ./deploy.sh --help for the options.
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
APP=/opt/cactusdb # where the image keeps the program, conn/ and export/

usage() {
  cat <<'EOF'
Usage: deploy.sh [--docker dd|k3s|k8s] [--env FILE] --install | --uninstall | --status

Actions:
  --install      build the image and deploy (again): an existing deployment is
                 replaced, its data is kept
  --uninstall    remove the deployment; the data is kept (see --purge)
  --status       show what is running and the URLs to open

Options:
  --docker T     where to deploy: dd (Docker / Docker Desktop / Podman), k3s or
                 k8s. Default: DEPLOY_TARGET from the env file, else dd; for
                 --uninstall and --status, what is found running.
  --env FILE     settings file (default: .env next to this script, if present);
                 see .env.example
  --purge        with --uninstall: also delete the image (dd) or the data
                 volumes (k3s/k8s PVCs). Asks first unless --yes is given.
                 Host folders are never deleted.
  --yes          don't ask for confirmation
  --from-source  compile cactusdb with Go instead of using the binaries in dist/
  -h, --help     show this help
EOF
}

die() { echo "deploy: $*" >&2; exit 1; }
say() { echo "$*"; }

TARGET='' ENV_FILE='' ACTION='' PURGE=no YES=no FROM_SOURCE=no
while [[ $# -gt 0 ]]; do
  case "$1" in
    --docker)      TARGET=${2:?--docker needs dd, k3s or k8s}; shift 2 ;;
    --docker=*)    TARGET=${1#*=}; shift ;;
    --env)         ENV_FILE=${2:?--env needs a file}; shift 2 ;;
    --env=*)       ENV_FILE=${1#*=}; shift ;;
    --install)     ACTION=install; shift ;;
    --uninstall)   ACTION=uninstall; shift ;;
    --status)      ACTION=status; shift ;;
    --purge)       PURGE=yes; shift ;;
    --yes|-y)      YES=yes; shift ;;
    --from-source) FROM_SOURCE=yes; shift ;;
    -h|--help)     usage; exit 0 ;;
    *)             usage >&2; die "unknown option: $1" ;;
  esac
done
[[ -n $ACTION ]] || { usage >&2; die "give --install, --uninstall or --status"; }
[[ $PURGE == no || $ACTION == uninstall ]] || die "--purge only goes with --uninstall"
# Ask before anything is removed, not halfway through.
[[ $PURGE == no || $YES == yes || -t 0 ]] || die "--purge deletes data: confirm with --yes when not running in a terminal"

# ---- settings -------------------------------------------------------------
# The env file is read as KEY=VALUE lines (not executed): # comments, blank
# lines, optional "export " and quotes are allowed. A variable already set in
# the environment wins over the file.
if [[ -z $ENV_FILE && -f $SCRIPT_DIR/.env ]]; then ENV_FILE=$SCRIPT_DIR/.env; fi
if [[ -n $ENV_FILE ]]; then
  [[ -f $ENV_FILE ]] || die "env file $ENV_FILE not found (copy .env.example to start one)"
  while IFS= read -r line || [[ -n $line ]]; do
    line=${line%$'\r'}
    [[ $line =~ ^[[:space:]]*(#|$) ]] && continue
    line=${line#export }
    [[ $line =~ ^[[:space:]]*([A-Za-z_][A-Za-z0-9_]*)[[:space:]]*=(.*)$ ]] || die "$ENV_FILE: can't read line: $line"
    key=${BASH_REMATCH[1]} val=${BASH_REMATCH[2]}
    val=${val#"${val%%[![:space:]]*}"}  # trim
    val=${val%"${val##*[![:space:]]}"}
    if [[ $val =~ ^\"([^\"]*)\"[[:space:]]*(#.*)?$ || $val =~ ^\'([^\']*)\'[[:space:]]*(#.*)?$ ]]; then
      val=${BASH_REMATCH[1]}
    else
      val=${val%%[[:space:]]#*} # an unquoted value ends at " #"
    fi
    [[ -n ${!key+x} ]] || printf -v "$key" '%s' "$val"
  done < "$ENV_FILE"
fi
# expand_home turns a leading ~ (as in the env file) into $HOME.
expand_home() {
  # shellcheck disable=SC2088 # matching a literal ~ is the point
  case $1 in "~") echo "$HOME" ;; "~/"*) echo "$HOME/${1#\~/}" ;; *) echo "$1" ;; esac
}

NAME=${CACTUSDB_NAME:-cactusdb}
IMAGE=${CACTUSDB_IMAGE:-cactusdb}
PORT=${CACTUSDB_PORT:-8080}
TZ_NAME=${CACTUSDB_TZ:-${TZ:-}}
PASSPHRASE=${CACTUSDB_PASSPHRASE:-}
RUNTIME=${CONTAINER_RUNTIME:-}
# dd
DD_BIND=${DD_BIND:-0.0.0.0}
DD_DATA_DIR=$(expand_home "${DD_DATA_DIR:-~/cactusdb}")
DD_EXPORT_DIR=$(expand_home "${DD_EXPORT_DIR:-$DD_DATA_DIR/export}")
DD_CONN_DIR=$(expand_home "${DD_CONN_DIR:-$DD_DATA_DIR/conn}")
DD_SQLITE_DIR=$(expand_home "${DD_SQLITE_DIR:-}")
# k3s / k8s
NAMESPACE=${K8S_NAMESPACE:-cactusdb}
KCONTEXT=${K8S_CONTEXT:-}
SERVICE_TYPE=${K8S_SERVICE_TYPE:-NodePort}
NODE_PORT=${K8S_NODE_PORT:-}
STORAGE=${K8S_STORAGE:-pvc}
STORAGE_CLASS=${K8S_STORAGE_CLASS:-}
CONN_SIZE=${K8S_CONN_SIZE:-1Gi}
EXPORT_SIZE=${K8S_EXPORT_SIZE:-10Gi}
HOST_CONN=${K8S_HOSTPATH_CONN:-/var/lib/cactusdb/conn}
HOST_EXPORT=${K8S_HOSTPATH_EXPORT:-/var/lib/cactusdb/export}
HOST_SQLITE=${K8S_HOSTPATH_SQLITE:-}
NODE=${K8S_NODE:-}
REGISTRY=${K8S_REGISTRY:-}
IMAGE_LOAD=${K8S_IMAGE_LOAD:-auto}
K3S_SSH=${K3S_SSH:-}
K3S_IMPORT=${K3S_IMPORT:-}
RUN_UID=${K8S_RUN_AS_UID:-1000}
RUN_GID=${K8S_RUN_AS_GID:-1000}

if ! [[ $PORT =~ ^[0-9]+$ ]] || (( PORT < 1 || PORT > 65535 )); then die "bad CACTUSDB_PORT: $PORT"; fi
[[ $NAME =~ ^[a-z0-9]([a-z0-9-]*[a-z0-9])?$ ]] || die "CACTUSDB_NAME must be lower-case letters, digits and dashes: $NAME"
[[ -z $NODE_PORT || $NODE_PORT =~ ^[0-9]+$ ]] || die "bad K8S_NODE_PORT: $NODE_PORT"
case $SERVICE_TYPE in NodePort|LoadBalancer|ClusterIP) ;; *) die "K8S_SERVICE_TYPE must be NodePort, LoadBalancer or ClusterIP" ;; esac
case $STORAGE in pvc|hostpath) ;; *) die "K8S_STORAGE must be pvc or hostpath" ;; esac
case $IMAGE_LOAD in auto|push|k3s|none) ;; *) die "K8S_IMAGE_LOAD must be auto, push, k3s or none" ;; esac

confirm() {
  [[ $YES == yes ]] && return 0
  [[ -t 0 ]] || die "$1 — rerun with --yes to confirm"
  local answer
  read -r -p "$1 [y/N] " answer
  [[ $answer == [yY]* ]]
}

# ---- container engine (building, and the dd target) --------------------
runtime() {
  if [[ -z $RUNTIME ]]; then
    if command -v docker >/dev/null 2>&1; then RUNTIME=docker
    elif command -v podman >/dev/null 2>&1; then RUNTIME=podman
    else die "neither docker nor podman is installed (needed to build the image)"; fi
  fi
  command -v "$RUNTIME" >/dev/null 2>&1 || die "$RUNTIME is not installed"
  "$RUNTIME" info >/dev/null 2>&1 || die "$RUNTIME is installed but not running"
}

# ---- kubectl --------------------------------------------------------------
KUBECTL_CMD=()
kube_init() {
  if [[ -n ${KUBECTL:-} ]]; then
    read -r -a KUBECTL_CMD <<< "$KUBECTL"
  elif command -v kubectl >/dev/null 2>&1; then
    KUBECTL_CMD=(kubectl)
  elif [[ $TARGET == k3s ]] && command -v k3s >/dev/null 2>&1; then
    KUBECTL_CMD=(sudo k3s kubectl)
  else
    die "kubectl not found (set KUBECTL in the env file, e.g. KUBECTL=\"sudo k3s kubectl\")"
  fi
  [[ -n $KCONTEXT ]] && KUBECTL_CMD+=(--context "$KCONTEXT")
  k version >/dev/null 2>&1 || die "can't reach the cluster with: ${KUBECTL_CMD[*]}"
}
k() { "${KUBECTL_CMD[@]}" "$@"; }
kn() { "${KUBECTL_CMD[@]}" -n "$NAMESPACE" "$@"; }
k_has() { kn get "$1" "$2" >/dev/null 2>&1; }

# ---- pick the target when not given ---------------------------------------
if [[ -z $TARGET ]]; then
  TARGET=${DEPLOY_TARGET:-}
fi
if [[ -z $TARGET && $ACTION != install ]]; then
  # Find what is deployed: a container named $NAME, else a Deployment.
  if { command -v docker >/dev/null 2>&1 && docker container inspect "$NAME" >/dev/null 2>&1; } ||
     { command -v podman >/dev/null 2>&1 && podman container inspect "$NAME" >/dev/null 2>&1; }; then
    TARGET="dd"
  elif command -v kubectl >/dev/null 2>&1 && kubectl ${KCONTEXT:+--context "$KCONTEXT"} -n "$NAMESPACE" get deployment "$NAME" >/dev/null 2>&1; then
    TARGET=k8s
  elif command -v k3s >/dev/null 2>&1; then
    TARGET=k3s
  else
    die "nothing named $NAME found; say where with --docker dd|k3s|k8s"
  fi
fi
TARGET=${TARGET:-dd}
case $TARGET in dd|k3s|k8s) ;; docker) TARGET="dd" ;; *) die "--docker must be dd, k3s or k8s, not $TARGET" ;; esac

VERSION=$(sed -n 's/^var version = "\(.*\)"/\1/p' "$SCRIPT_DIR/main.go" 2>/dev/null || true)
VERSION=${VERSION:-latest}

# ---- building the image ---------------------------------------------------
# build_image ARCH... builds $IMAGE_REF for the given architectures. One
# architecture is built into the local engine; several need docker buildx
# and are pushed straight to the registry.
build_image() {
  local ctx arch root
  ctx=$(mktemp -d)
  # shellcheck disable=SC2064 # expand now: ctx is local
  trap "rm -rf '$ctx'" EXIT
  for arch in "$@"; do
    root=$ctx/rootfs-$arch$APP
    mkdir -p "$root/conn" "$root/export"
    chmod 0777 "$root/conn" "$root/export"
    if [[ $FROM_SOURCE == yes ]]; then
      command -v go >/dev/null 2>&1 || die "--from-source needs Go installed"
      say "Compiling cactusdb $VERSION for linux/${arch}…"
      (cd "$SCRIPT_DIR" && CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
        -ldflags "-s -w -X main.version=$VERSION" -o "$root/cactusdb" .)
    else
      local bin=$SCRIPT_DIR/dist/cactusdb-linux-$arch
      [[ -f $bin ]] || die "$bin not found: run ./build.sh first, or use --from-source"
      cp "$bin" "$root/cactusdb"
    fi
    chmod 0755 "$root/cactusdb"
  done
  # Only the binaries and the Dockerfile: nothing else from dist/ (such as a
  # cactus.conn.db or cactus.key) can end up in the image.
  cp "$SCRIPT_DIR/Dockerfile" "$ctx/Dockerfile"
  if [[ $# -gt 1 ]]; then
    local platforms
    platforms=$(printf 'linux/%s,' "$@")
    say "Building and pushing $IMAGE_REF (${platforms%,})…"
    docker buildx build -q --platform "${platforms%,}" -t "$IMAGE_REF" --push "$ctx" >/dev/null ||
      die "multi-architecture builds need docker buildx and a registry (K8S_REGISTRY)"
  else
    say "Building image $IMAGE_REF (linux/$1)…"
    "$RUNTIME" build -q --platform "linux/$1" -t "$IMAGE_REF" "$ctx" >/dev/null
  fi
  rm -rf "$ctx"
  trap - EXIT
}

norm_arch() {
  case $1 in x86_64|amd64) echo amd64 ;; aarch64|arm64) echo arm64 ;; *) die "unsupported architecture: $1" ;; esac
}

# ========================================================================
# dd: Docker / Docker Desktop / Podman
# ========================================================================
dd_exists() { "$RUNTIME" container inspect "$NAME" >/dev/null 2>&1; }

dd_urls() {
  local ips ip
  if [[ $DD_BIND == 0.0.0.0 ]]; then
    echo "  open http://localhost:$PORT"
    if command -v ip >/dev/null 2>&1; then
      ips=$(ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1)
    elif command -v ifconfig >/dev/null 2>&1; then
      ips=$(ifconfig | awk '/inet / && $2 != "127.0.0.1" {print $2}')
    fi
  else
    ips=$DD_BIND
  fi
  for ip in ${ips:-}; do # leave out container bridge networks
    case $ip in 172.1[7-9].*|172.2[0-9].*|172.3[01].*|10.88.*) ;; *) echo "  open http://$ip:$PORT" ;; esac
  done
}

dd_install() {
  runtime
  local arch
  arch=$(norm_arch "$("$RUNTIME" info --format '{{.Architecture}}' 2>/dev/null || uname -m)")
  IMAGE_REF=$IMAGE:$VERSION
  build_image "$arch"
  "$RUNTIME" tag "$IMAGE_REF" "$IMAGE:latest"

  mkdir -p "$DD_EXPORT_DIR" "$DD_CONN_DIR"
  DD_EXPORT_DIR=$(cd "$DD_EXPORT_DIR" && pwd)
  DD_CONN_DIR=$(cd "$DD_CONN_DIR" && pwd)
  chmod 0700 "$DD_CONN_DIR" # credentials and their key live here
  local z=
  if command -v selinuxenabled >/dev/null 2>&1 && selinuxenabled; then z=,Z; fi # label for SELinux
  local args=(-v "$DD_CONN_DIR:$APP/conn:rw$z" -v "$DD_EXPORT_DIR:$APP/export:rw$z" -e "CACTUSDB_PORT=$PORT")
  if [[ -n $DD_SQLITE_DIR ]]; then
    [[ -d $DD_SQLITE_DIR ]] || die "DD_SQLITE_DIR $DD_SQLITE_DIR is not a folder"
    args+=(-v "$(cd "$DD_SQLITE_DIR" && pwd):/sqlite:ro$z")
  fi
  [[ -n $TZ_NAME ]] && args+=(-e "TZ=$TZ_NAME")
  [[ -n $PASSPHRASE ]] && args+=(-e "CACTUSDB_PASSPHRASE=$PASSPHRASE")
  # As the calling user, so the files on the host are yours (rootless podman
  # maps root to you already).
  [[ $RUNTIME == podman ]] || args+=(--user "$(id -u):$(id -g)")

  if dd_exists; then
    say "Replacing the existing container ${NAME}…"
    "$RUNTIME" rm -f "$NAME" >/dev/null
  fi
  "$RUNTIME" run -d --name "$NAME" --restart unless-stopped --label app=cactusdb \
    "${args[@]}" -p "$DD_BIND:$PORT:$PORT" "$IMAGE_REF" >/dev/null

  local i
  for i in $(seq 1 30); do
    [[ $("$RUNTIME" inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null) == true ]] ||
      { "$RUNTIME" logs "$NAME" >&2 || true; die "the container stopped; its log is above"; }
    if "$RUNTIME" exec "$NAME" wget -q -O /dev/null "http://127.0.0.1:$PORT/api/info" 2>/dev/null; then
      say "$("$RUNTIME" exec "$NAME" cactusdb --version) is running in container $NAME ($RUNTIME)"
      dd_urls
      say "  saved connections: $DD_CONN_DIR/cactus.conn.db (key: $DD_CONN_DIR/cactus.key)"
      say "  exports:           $DD_EXPORT_DIR/{json,csv,xlsx,xml,sql,backup}"
      [[ -n $DD_SQLITE_DIR ]] && say "  SQLite files:      $DD_SQLITE_DIR, open them as /sqlite/<file>"
      return 0
    fi
    [[ $i -lt 30 ]] && sleep 1
  done
  "$RUNTIME" logs "$NAME" >&2 || true
  die "cactusdb did not answer within 30 seconds; its log is above"
}

dd_uninstall() {
  runtime
  if dd_exists; then
    local kept
    kept=$("$RUNTIME" inspect -f '{{range .Mounts}}{{if eq .Type "bind"}}{{.Source}} {{end}}{{end}}' "$NAME")
    "$RUNTIME" rm -f "$NAME" >/dev/null
    say "Removed container $NAME. Your data is kept in: ${kept% }"
  else
    say "No container named $NAME"
  fi
  if [[ $PURGE == yes ]]; then
    if confirm "Delete the $IMAGE images?"; then
      local img
      for img in $("$RUNTIME" image ls --format '{{.Repository}}:{{.Tag}}' | grep "^$IMAGE:" || true); do
        "$RUNTIME" image rm "$img" >/dev/null 2>&1 || true
      done
      say "Removed the $IMAGE images. The data folders are never deleted; remove them yourself if you no longer need them."
    fi
  fi
}

dd_status() {
  runtime
  dd_exists || die "no container named $NAME"
  "$RUNTIME" ps -a --filter "name=^${NAME}$" --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}'
  PORT=$("$RUNTIME" inspect -f '{{range .Config.Env}}{{println .}}{{end}}' "$NAME" | sed -n 's/^CACTUSDB_PORT=//p')
  dd_urls
}

# ========================================================================
# k3s / k8s
# ========================================================================
# labels N prints the labels, indented by N spaces.
labels() {
  local pad
  pad=$(printf '%*s' "$1" '')
  printf '%sapp.kubernetes.io/name: cactusdb\n%sapp.kubernetes.io/instance: %s\n%sapp.kubernetes.io/managed-by: deploy.sh\n' \
    "$pad" "$pad" "$NAME" "$pad"
}

# k8s_manifest writes the resources to deploy as YAML.
k8s_manifest() {
  local pull=$1
  # A namespace that already exists is left as it is; one created here is
  # labelled, so --uninstall knows it may remove it.
  if ! k get namespace "$NAMESPACE" >/dev/null 2>&1; then
    cat <<EOF
apiVersion: v1
kind: Namespace
metadata:
  name: $NAMESPACE
  labels:
    app.kubernetes.io/managed-by: deploy.sh
---
EOF
  fi
  if [[ -n $PASSPHRASE ]]; then
    cat <<EOF
apiVersion: v1
kind: Secret
metadata:
  name: $NAME
  namespace: $NAMESPACE
  labels:
$(labels 4)
type: Opaque
stringData:
  CACTUSDB_PASSPHRASE: $(yaml_str "$PASSPHRASE")
---
EOF
  fi
  if [[ $STORAGE == pvc ]]; then
    local which size
    for which in conn export; do
      size=$CONN_SIZE; [[ $which == export ]] && size=$EXPORT_SIZE
      cat <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: $NAME-$which
  namespace: $NAMESPACE
  labels:
$(labels 4)
spec:
  accessModes: [ReadWriteOnce]
  resources:
    requests:
      storage: $size
EOF
      [[ -n $STORAGE_CLASS ]] && echo "  storageClassName: $STORAGE_CLASS"
      echo "---"
    done
  fi
  cat <<EOF
apiVersion: apps/v1
kind: Deployment
metadata:
  name: $NAME
  namespace: $NAMESPACE
  labels:
$(labels 4)
spec:
  replicas: 1                 # one writer for cactus.conn.db
  strategy:
    type: Recreate            # the old pod lets go of the volumes first
  selector:
    matchLabels:
      app.kubernetes.io/instance: $NAME
  template:
    metadata:
      labels:
$(labels 8)
    spec:
      securityContext:
        fsGroup: $RUN_GID
EOF
  [[ -n $NODE ]] && cat <<EOF
      nodeSelector:
        kubernetes.io/hostname: $(yaml_str "$NODE")
EOF
  cat <<EOF
      initContainers:
        # The volumes may start out owned by root: hand them to cactusdb.
        - name: fix-permissions
          image: $IMAGE_REF
          imagePullPolicy: $pull
          command: [sh, -c, "chown -R $RUN_UID:$RUN_GID $APP/conn && chmod 0700 $APP/conn && chown $RUN_UID:$RUN_GID $APP/export"]
          securityContext:
            runAsUser: 0
          volumeMounts:
            - { name: conn, mountPath: $APP/conn }
            - { name: export, mountPath: $APP/export }
      containers:
        - name: cactusdb
          image: $IMAGE_REF
          imagePullPolicy: $pull
          env:
            - { name: CACTUSDB_PORT, value: "$PORT" }
EOF
  [[ -n $TZ_NAME ]] && echo "            - { name: TZ, value: $(yaml_str "$TZ_NAME") }"
  [[ -n $PASSPHRASE ]] && cat <<EOF
            - name: CACTUSDB_PASSPHRASE
              valueFrom: { secretKeyRef: { name: $NAME, key: CACTUSDB_PASSPHRASE } }
EOF
  cat <<EOF
          ports:
            - { name: http, containerPort: $PORT }
          securityContext:
            runAsUser: $RUN_UID
            runAsGroup: $RUN_GID
            runAsNonRoot: true
            allowPrivilegeEscalation: false
            readOnlyRootFilesystem: true
            capabilities: { drop: [ALL] }
          readinessProbe:
            httpGet: { path: /api/info, port: http }
            periodSeconds: 10
          livenessProbe:
            httpGet: { path: /api/info, port: http }
            initialDelaySeconds: 10
            periodSeconds: 30
          resources:
            requests: { cpu: 50m, memory: 64Mi }
            limits: { memory: 1Gi }
          volumeMounts:
            - { name: conn, mountPath: $APP/conn }
            - { name: export, mountPath: $APP/export }
            - { name: tmp, mountPath: /tmp }
EOF
  [[ -n $HOST_SQLITE ]] && echo "            - { name: sqlite, mountPath: /sqlite, readOnly: true }"
  echo "      volumes:"
  if [[ $STORAGE == pvc ]]; then
    cat <<EOF
        - { name: conn, persistentVolumeClaim: { claimName: $NAME-conn } }
        - { name: export, persistentVolumeClaim: { claimName: $NAME-export } }
EOF
  else
    cat <<EOF
        - { name: conn, hostPath: { path: $(yaml_str "$HOST_CONN"), type: DirectoryOrCreate } }
        - { name: export, hostPath: { path: $(yaml_str "$HOST_EXPORT"), type: DirectoryOrCreate } }
EOF
  fi
  echo "        - { name: tmp, emptyDir: {} }"
  [[ -n $HOST_SQLITE ]] && echo "        - { name: sqlite, hostPath: { path: $(yaml_str "$HOST_SQLITE"), type: Directory } }"
  cat <<EOF
---
apiVersion: v1
kind: Service
metadata:
  name: $NAME
  namespace: $NAMESPACE
  labels:
$(labels 4)
spec:
  type: $SERVICE_TYPE
  selector:
    app.kubernetes.io/instance: $NAME
  ports:
    - name: http
      port: $PORT
      targetPort: http
EOF
  [[ $SERVICE_TYPE == NodePort && -n $NODE_PORT ]] && echo "      nodePort: $NODE_PORT"
  return 0
}

# yaml_str quotes a value for YAML (double quotes, escaped).
yaml_str() {
  local s=${1//\\/\\\\}
  s=${s//\"/\\\"}
  printf '"%s"' "$s"
}

# load_image gets $IMAGE_REF onto the cluster's nodes.
load_image() {
  local mode=$IMAGE_LOAD
  if [[ $mode == auto ]]; then
    if [[ -n $REGISTRY ]]; then mode=push
    elif [[ $TARGET == k3s ]]; then mode=k3s
    else mode=none; fi
  fi
  case $mode in
    push)
      [[ -n $REGISTRY ]] || die "K8S_IMAGE_LOAD=push needs K8S_REGISTRY"
      [[ ${#ARCHES[@]} -gt 1 ]] && return 0 # buildx pushed it already
      say "Pushing $IMAGE_REF…"
      "$RUNTIME" push -q "$IMAGE_REF" >/dev/null ;;
    k3s)
      # k3s runs its own containerd: import the image into it.
      if [[ -n $K3S_IMPORT ]]; then
        say "Importing $IMAGE_REF into k3s ($K3S_IMPORT)…"
        "$RUNTIME" save "$IMAGE_REF" | sh -c "$K3S_IMPORT" >/dev/null
      elif [[ -n $K3S_SSH ]]; then
        say "Importing $IMAGE_REF into k3s on $K3S_SSH…"
        "$RUNTIME" save "$IMAGE_REF" | ssh "$K3S_SSH" 'sudo k3s ctr images import -' >/dev/null
      elif command -v k3s >/dev/null 2>&1; then
        say "Importing $IMAGE_REF into k3s…"
        "$RUNTIME" save "$IMAGE_REF" | sudo k3s ctr images import - >/dev/null
      else
        die "can't load the image into k3s: k3s isn't on this machine. Set K3S_SSH=user@k3s-host, K3S_IMPORT, or K8S_REGISTRY"
      fi ;;
    none) ;; # the nodes see the local images (e.g. Docker Desktop)
  esac
  LOADED=$mode
}

# k8s_urls prints where to open cactusdb. Each address is tried from here;
# when none answers (e.g. Docker Desktop in kind mode, or a firewall), it
# shows how to reach it with kubectl port-forward instead.
k8s_urls() {
  local type np ip lb urls=() u ok=no
  type=$(kn get service "$NAME" -o jsonpath='{.spec.type}')
  case $type in
    NodePort|LoadBalancer)
      np=$(kn get service "$NAME" -o jsonpath='{.spec.ports[0].nodePort}')
      if [[ $type == LoadBalancer ]]; then
        lb=$(kn get service "$NAME" -o jsonpath='{.status.loadBalancer.ingress[0].ip}{.status.loadBalancer.ingress[0].hostname}')
        if [[ -n $lb ]]; then urls+=("http://$lb:$PORT"); else echo "  (no load-balancer address yet; $0 --status shows it once assigned)"; fi
      fi
      if [[ -n $np ]]; then
        for ip in $(k get nodes -o jsonpath='{range .items[*]}{range .status.addresses[?(@.type=="InternalIP")]}{.address}{" "}{end}{end}') localhost; do
          urls+=("http://$ip:$np")
        done
      fi ;;
  esac
  for u in ${urls[@]+"${urls[@]}"}; do
    if command -v curl >/dev/null 2>&1 && curl -fsS -m 3 -o /dev/null "$u/api/info" 2>/dev/null; then
      echo "  open $u"; ok=yes
    elif [[ $u != http://localhost:* ]]; then
      echo "  open $u   (not reachable from this machine)"
    fi
  done
  if [[ $type == ClusterIP ]]; then
    echo "  inside the cluster: http://$NAME.$NAMESPACE.svc:$PORT"
  fi
  if [[ $ok == no ]]; then
    echo "  from here: ${KUBECTL_CMD[*]} -n $NAMESPACE port-forward --address 0.0.0.0 service/$NAME $PORT:$PORT"
    echo "             then open http://localhost:$PORT"
  fi
}

k8s_install() {
  kube_init
  runtime
  # Build for the architecture(s) of the node(s) cactusdb can run on.
  local sel=() a
  [[ -n $NODE ]] && sel=(-l "kubernetes.io/hostname=$NODE")
  ARCHES=()
  for a in $(k get nodes ${sel[@]+"${sel[@]}"} -o jsonpath='{range .items[*]}{.status.nodeInfo.architecture}{"\n"}{end}' | sort -u); do
    ARCHES+=("$(norm_arch "$a")")
  done
  [[ ${#ARCHES[@]} -gt 0 ]] || die "no nodes found${NODE:+ named $NODE}"
  if [[ ${#ARCHES[@]} -gt 1 && -z $REGISTRY ]]; then
    die "the nodes have several architectures (${ARCHES[*]}): set K8S_REGISTRY to push a multi-architecture image, or K8S_NODE to pin cactusdb to one node"
  fi
  if [[ $STORAGE == hostpath && -z $NODE && $(k get nodes -o name | wc -l) -gt 1 ]]; then
    die "K8S_STORAGE=hostpath on a cluster with several nodes needs K8S_NODE, so cactusdb always finds its folders"
  fi

  # A new tag each time, so the Deployment rolls out the new image.
  local tag
  tag=$VERSION-$(date +%Y%m%d%H%M%S)
  IMAGE_REF=$IMAGE:$tag
  [[ -n $REGISTRY ]] && IMAGE_REF=${REGISTRY%/}/$IMAGE:$tag
  build_image "${ARCHES[@]}"
  load_image
  local pull=IfNotPresent
  [[ $LOADED == push ]] && pull=Always

  say "Deploying to namespace $NAMESPACE (${KUBECTL_CMD[*]})…"
  k8s_manifest "$pull" | k apply -f - >/dev/null
  if ! kn rollout status "deployment/$NAME" --timeout=180s >/dev/null; then
    kn get pods -l "app.kubernetes.io/instance=$NAME" >&2 || true
    kn describe pods -l "app.kubernetes.io/instance=$NAME" 2>/dev/null | tail -20 >&2 || true
    kn logs "deployment/$NAME" --tail=20 >&2 2>/dev/null || true
    die "the deployment did not become ready; details are above"
  fi
  say "$(kn exec "deployment/$NAME" -c cactusdb -- cactusdb --version 2>/dev/null || echo cactusdb) is running in $TARGET (namespace $NAMESPACE, image $IMAGE_REF)"
  k8s_urls
  if [[ $STORAGE == pvc ]]; then
    say "  saved connections: volume $NAME-conn ($CONN_SIZE)    exports: volume $NAME-export ($EXPORT_SIZE)"
  else
    say "  saved connections: ${NODE:-node}:$HOST_CONN    exports: ${NODE:-node}:$HOST_EXPORT"
  fi
  say "  log:  ${KUBECTL_CMD[*]} -n $NAMESPACE logs -f deployment/$NAME"
}

k8s_uninstall() {
  kube_init
  if ! k get namespace "$NAMESPACE" >/dev/null 2>&1; then
    say "Namespace $NAMESPACE doesn't exist: nothing to remove"
    return 0
  fi
  local found=no kind
  for kind in deployment service secret; do
    k_has "$kind" "$NAME" || continue
    found=yes
    if ! kn delete "$kind" "$NAME" --timeout=60s >/dev/null 2>&1; then
      say "warning: $kind/$NAME is still being deleted after 60 seconds."
      if [[ $kind == service ]]; then
        # A LoadBalancer Service waits until the cloud has released its load
        # balancer; without a load-balancer controller that never happens.
        say "  If this cluster has no load-balancer controller, release it with:"
        say "  ${KUBECTL_CMD[*]} -n $NAMESPACE patch service $NAME --type=merge -p '{\"metadata\":{\"finalizers\":null}}'"
      fi
    fi
  done
  if [[ $found == yes ]]; then say "Removed $NAME from namespace $NAMESPACE"; else say "No $NAME found in namespace $NAMESPACE"; fi

  local pvcs
  pvcs=$(kn get pvc -l "app.kubernetes.io/instance=$NAME" -o name 2>/dev/null || true)
  if [[ -n $pvcs ]]; then
    if [[ $PURGE == yes ]] && confirm "Delete the volumes with the saved connections and exports ($(echo "$pvcs" | tr '\n' ' '))? This can't be undone."; then
      # shellcheck disable=SC2086 # one name per word
      kn delete $pvcs --timeout=120s >/dev/null || say "warning: the volumes are still being deleted"
      say "Deleted the data volumes"
    else
      say "Kept the data volumes: $(echo "$pvcs" | sed 's|^persistentvolumeclaim/||' | tr '\n' ' ')(--purge deletes them)"
    fi
  elif [[ $STORAGE == hostpath ]]; then
    say "The data on the node is kept: $HOST_CONN and $HOST_EXPORT"
  fi
  # A namespace this script created goes once it holds nothing else.
  if [[ $(k get namespace "$NAMESPACE" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}') == deploy.sh &&
        -z $(kn get all,pvc,secret,configmap -o name 2>/dev/null | grep -v '^configmap/kube-root-ca.crt$' || true) ]]; then
    k delete namespace "$NAMESPACE" --wait=false >/dev/null && say "Removed the empty namespace $NAMESPACE"
  fi
}

k8s_status() {
  kube_init
  k_has deployment "$NAME" || die "no deployment $NAME in namespace $NAMESPACE"
  kn get deployment,pods,service,pvc -l "app.kubernetes.io/instance=$NAME"
  k8s_urls
}

# ---- go -------------------------------------------------------------------
case $TARGET:$ACTION in
  dd:install)    dd_install ;;
  dd:uninstall)  dd_uninstall ;;
  dd:status)     dd_status ;;
  *:install)     k8s_install ;;
  *:uninstall)   k8s_uninstall ;;
  *:status)      k8s_status ;;
esac
