#!/usr/bin/env bash
#
# Run the Prometheus the dashboards read, on the fleet host.
#
#   ops/prometheus.sh up       # fetch the pinned release if needed, write the scrape config, start
#   ops/prometheus.sh down
#   ops/prometheus.sh status   # whether it is up, and whether its targets answer
#   ops/prometheus.sh config   # print the scrape config it would start with
#
# It is for watching a run and never for measuring one (idea.md §6). Nothing in
# a sweep reads it, and the router and the replicas only answer its scrapes —
# they never push to it — so it can be stopped, or never started, without
# changing a single cell.
#
# Three things this script is careful about:
#
# 1. It listens on loopback only, and refuses a port anyone already holds. The
#    box is shared and a port belongs to whoever took it first: 9090 is another
#    user's Prometheus, and 9091, the port idea.md first planned for this one,
#    had been taken by someone else by 2026-09-10. Loopback means the only way
#    in is an ssh tunnel (ops/dashboards.sh), so nothing here is exposed to the
#    network.
#
# 2. It scrapes every replica directly and labels it with the id the router
#    gives it, so the engine's KV utilization and the router's inflight for one
#    replica land on one label and a panel can draw them together. The config is
#    written from `ops/fleet.sh replicas` at every start, so it cannot name a
#    replica the fleet no longer runs: GPU 3 is out of the fleet (#25), and a
#    hand-written list of six would scrape a card nothing is served from.
#
# 3. Its storage is bounded. /home is shared, and a TSDB left to grow across a
#    week of sweeps is somebody else's full disk.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(dirname "$here")"
# shellcheck source=versions.env
source "$here/versions.env"
# shellcheck source=observability.env
source "$here/observability.env"

run_dir="$repo/$RUN_DIR"
pid_file="$run_dir/prometheus.pid"
log_file="$run_dir/prometheus.log"
config_file="$run_dir/prometheus.yml"
data_dir="$run_dir/prometheus"

die() { echo "prometheus: $*" >&2; exit 1; }

# platform names this machine the way Prometheus names its release archives.
platform() {
  local os arch
  os="$(uname -s | tr 'A-Z' 'a-z')"
  case "$(uname -m)" in
    x86_64 | amd64) arch=amd64 ;;
    arm64 | aarch64) arch=arm64 ;;
    *) die "no Prometheus build is pinned for $(uname -m)" ;;
  esac
  echo "$os-$arch"
}

binary() { echo "$PROMETHEUS_HOME/prometheus-$PROMETHEUS_VERSION.$(platform)/prometheus"; }

sha256() {
  if command -v sha256sum >/dev/null; then
    sha256sum "$1" | cut -d' ' -f1
  else
    shasum -a 256 "$1" | cut -d' ' -f1
  fi
}

# install_release fetches the pinned release and checks it against the pinned
# checksum, once. The checksum is pinned here rather than read from the release's
# own sha256sums.txt, because a file fetched from the same place as the archive
# vouches for nothing the archive does not.
install_release() {
  local bin plat var want tarball url got
  bin="$(binary)"
  [[ -x "$bin" ]] && return 0

  plat="$(platform)"
  var="PROMETHEUS_SHA256_${plat//-/_}"
  want="${!var:-}"
  [[ -n "$want" ]] || die "no checksum pinned for $plat. Add $var to ops/observability.env from the release's sha256sums.txt."
  tarball="prometheus-$PROMETHEUS_VERSION.$plat.tar.gz"
  url="https://github.com/prometheus/prometheus/releases/download/v$PROMETHEUS_VERSION/$tarball"

  mkdir -p "$PROMETHEUS_HOME"
  echo "prometheus: fetching $url"
  curl -fsSL --retry 3 -o "$PROMETHEUS_HOME/$tarball" "$url" || die "could not download $url"
  got="$(sha256 "$PROMETHEUS_HOME/$tarball")"
  if [[ "$got" != "$want" ]]; then
    rm -f "$PROMETHEUS_HOME/$tarball"
    die "$tarball has checksum $got, but $want is pinned. Refusing to run it."
  fi
  tar -xzf "$PROMETHEUS_HOME/$tarball" -C "$PROMETHEUS_HOME"
  rm -f "$PROMETHEUS_HOME/$tarball"
  [[ -x "$bin" ]] || die "the release did not unpack to $bin"
}

# replica_specs prints the fleet one id=url per line, exactly as the router is
# given it. PROMETHEUS_REPLICAS overrides it, for a stack pointed at fake
# replicas.
replica_specs() {
  local specs="${PROMETHEUS_REPLICAS:-}"
  if [[ -z "$specs" ]]; then
    specs="$("$here/fleet.sh" replicas)"
  fi
  tr ',' '\n' <<< "$specs"
}

# config prints the scrape configuration.
config() {
  cat <<EOF
# Written by ops/prometheus.sh at every start. Change ops/observability.env, not this.
global:
  scrape_interval: $PROMETHEUS_SCRAPE_INTERVAL
  scrape_timeout: $PROMETHEUS_SCRAPE_TIMEOUT
scrape_configs:
  - job_name: router
    static_configs:
      - targets: ['$ROUTER_ADDR']
  - job_name: replicas
    static_configs:
EOF
  local spec id target any=0
  while IFS= read -r spec; do
    [[ -n "$spec" ]] || continue
    [[ "$spec" == *=* ]] \
      || die "replica spec '$spec' names no id. Every target is labelled with the id the router knows it by, or the panels cannot put a replica's KV beside its inflight."
    id="${spec%%=*}"
    target="${spec#*=}"
    target="${target#*://}"
    target="${target%%/*}"
    printf "      - targets: ['%s']\n        labels:\n          replica: '%s'\n" "$target" "$id"
    any=1
  done < <(replica_specs)
  (( any )) || die "no replicas to scrape"
}

# port_taken reports whether anything is listening on the port, on any address.
#
# Checked here rather than left to the bind, because the two platforms this runs
# on disagree about it: Linux refuses 127.0.0.1:p while someone holds *:p, and
# macOS lets both bind and hands each connection to whichever it pleases.
port_taken() {
  if command -v ss >/dev/null; then
    ss -ltnH "sport = :$1" | grep -q .
  else
    lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
  fi
}

running() { [[ -f "$pid_file" ]] && kill -0 "$(cat "$pid_file")" 2>/dev/null; }

up() {
  mkdir -p "$run_dir" "$data_dir"
  if running; then
    die "already running as PID $(cat "$pid_file"). Run ops/prometheus.sh down first."
  fi
  if port_taken "$PROMETHEUS_PORT"; then
    die "port $PROMETHEUS_PORT is already held by another process. The box is shared: pick a free PROMETHEUS_PORT in ops/observability.env rather than taking this one back."
  fi
  install_release
  config >"$config_file"

  nohup "$(binary)" \
    --config.file="$config_file" \
    --storage.tsdb.path="$data_dir" \
    --storage.tsdb.retention.time="$PROMETHEUS_RETENTION_TIME" \
    --storage.tsdb.retention.size="$PROMETHEUS_RETENTION_SIZE" \
    --web.listen-address="127.0.0.1:$PROMETHEUS_PORT" \
    </dev/null >"$log_file" 2>&1 &
  echo $! >"$pid_file"

  local deadline=$(( SECONDS + 60 ))
  until curl -sf "http://127.0.0.1:$PROMETHEUS_PORT/-/ready" >/dev/null; do
    running || die "exited during startup. Last lines of $log_file:
$(tail -n 20 "$log_file")"
    (( SECONDS < deadline )) || die "not ready within 60s. See $log_file"
    sleep 1
  done
  echo "prometheus: up on 127.0.0.1:$PROMETHEUS_PORT (PID $(cat "$pid_file")), scraping the router at $ROUTER_ADDR and $(replica_specs | grep -c .) replicas every $PROMETHEUS_SCRAPE_INTERVAL"
  echo "prometheus: watch it from the workstation with: make dashboards-up"
}

down() {
  [[ -f "$pid_file" ]] || { echo "prometheus is not running"; return 0; }
  local target
  target="$(cat "$pid_file")"
  if kill -0 "$target" 2>/dev/null; then
    echo "stopping prometheus (PID $target)"
    kill "$target"
    # SIGTERM makes it flush its head block, which is worth the wait.
    for _ in $(seq 1 30); do
      kill -0 "$target" 2>/dev/null || break
      sleep 1
    done
    if kill -0 "$target" 2>/dev/null; then kill -9 "$target" || true; fi
  fi
  rm -f "$pid_file"
}

status() {
  if ! running; then
    echo "prometheus is not running"
    return 0
  fi
  echo "prometheus PID $(cat "$pid_file") on 127.0.0.1:$PROMETHEUS_PORT"
  local targets
  if ! targets="$(curl -sf "http://127.0.0.1:$PROMETHEUS_PORT/api/v1/targets?state=active")"; then
    echo "  not answering its API"
    return 0
  fi
  # Counted with grep rather than read with jq, which the box does not have.
  echo "$targets" | grep -o '"health":"[a-z]*"' | cut -d'"' -f4 | sort | uniq -c | sed 's/^ */  targets /'
  echo "$targets" | grep -o '"lastError":"[^"][^"]*"' | cut -d'"' -f4 | sort -u | sed 's/^/  last error: /' || true
}

case "${1:-}" in
  up)     up ;;
  down)   down ;;
  status) status ;;
  config) config ;;
  *)      die "usage: $0 up|down|status|config" ;;
esac
