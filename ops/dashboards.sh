#!/usr/bin/env bash
#
# Watch a run from the workstation: an ssh tunnel to the fleet host's
# Prometheus, and a Grafana on this machine with the five panels provisioned.
#
#   ops/dashboards.sh up       # open the tunnel, start Grafana, print where it is
#   ops/dashboards.sh down
#   ops/dashboards.sh status
#
# Grafana runs here rather than on the fleet host because that host is shared
# and carries nothing the measurement does not need. It runs in Docker so that
# it is not installed here either: the image is pinned in
# ops/observability.env, and `down` leaves nothing behind but the image.
#
# The tunnel is an ssh control master rather than a backgrounded ssh with a pid
# file. `ssh -f` forks once it has authenticated, so the pid of the command that
# started it is not the pid holding the tunnel; the control socket is how ssh
# itself names the connection, to check it and to close it.
#
# Set GPU_HOST empty to skip the tunnel and read a Prometheus on this machine,
# which is how the stack is smoke-tested against fake replicas.

set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=observability.env
source "$here/observability.env"

# In the temp directory rather than the repository: it is this machine's
# connection rather than project state, and keeping it in the run directory would
# mean a second copy of versions.env's RUN_DIR.
socket="${TMPDIR:-/tmp}"
socket="${socket%/}/kvroute-dashboards-tunnel.sock"
container="kvroute-grafana"
prometheus="http://127.0.0.1:$PROMETHEUS_PORT"
dashboard="http://127.0.0.1:$GRAFANA_PORT/d/kvroute"

die() { echo "dashboards: $*" >&2; exit 1; }

port_taken() { lsof -nP -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1; }

tunnel_open() { [[ -n "$GPU_HOST" ]] && ssh -S "$socket" -O check "$GPU_HOST" >/dev/null 2>&1; }

tunnel_up() {
  if [[ -z "$GPU_HOST" ]]; then
    echo "dashboards: GPU_HOST is empty, so no tunnel; Grafana reads the Prometheus on this machine's port $PROMETHEUS_PORT"
    return 0
  fi
  if tunnel_open; then
    echo "dashboards: tunnel to $GPU_HOST already open"
    return 0
  fi
  if port_taken "$PROMETHEUS_PORT"; then
    die "port $PROMETHEUS_PORT is already in use on this machine, so the tunnel cannot take it. Free it, or change PROMETHEUS_PORT in ops/observability.env for both ends."
  fi
  mkdir -p "$(dirname "$socket")"
  ssh -f -N -M -S "$socket" \
    -o ExitOnForwardFailure=yes -o ServerAliveInterval=30 -o ServerAliveCountMax=3 \
    -L "127.0.0.1:$PROMETHEUS_PORT:127.0.0.1:$PROMETHEUS_PORT" "$GPU_HOST" \
    || die "could not open a tunnel to $GPU_HOST"
  echo "dashboards: tunnel open, 127.0.0.1:$PROMETHEUS_PORT here to 127.0.0.1:$PROMETHEUS_PORT on $GPU_HOST"
}

grafana_running() { [[ -n "$(docker ps -q --filter "name=^${container}\$" 2>/dev/null)" ]]; }

grafana_up() {
  command -v docker >/dev/null || die "docker is not installed. Grafana runs in Docker so that nothing is installed on this machine for it."
  docker info >/dev/null 2>&1 || die "Docker is not running. Start Docker Desktop, then run this again."
  if grafana_running; then
    echo "dashboards: grafana already running"
    return 0
  fi
  # A stopped container under the same name would make `docker run` refuse.
  docker rm -f "$container" >/dev/null 2>&1 || true
  if port_taken "$GRAFANA_PORT"; then
    die "port $GRAFANA_PORT is already in use on this machine; set GRAFANA_PORT to a free one"
  fi
  # Anonymous, and a viewer only: it listens on this machine's loopback, and the
  # dashboards are provisioned read-only, so viewing is all anyone needs.
  # host.docker.internal is Docker Desktop's name for this machine, which is
  # where the tunnel's end is.
  docker run -d --name "$container" \
    -p "127.0.0.1:$GRAFANA_PORT:3000" \
    -e GF_AUTH_ANONYMOUS_ENABLED=true \
    -e GF_AUTH_ANONYMOUS_ORG_ROLE=Viewer \
    -e GF_AUTH_DISABLE_LOGIN_FORM=true \
    -e GF_ANALYTICS_REPORTING_ENABLED=false \
    -e GF_ANALYTICS_CHECK_FOR_UPDATES=false \
    -e GF_DASHBOARDS_DEFAULT_HOME_DASHBOARD_PATH=/var/lib/grafana/dashboards/kvroute.json \
    -e KVROUTE_PROMETHEUS_URL="http://host.docker.internal:$PROMETHEUS_PORT" \
    -e KVROUTE_SCRAPE_INTERVAL="$PROMETHEUS_SCRAPE_INTERVAL" \
    -v "$here/grafana/provisioning:/etc/grafana/provisioning:ro" \
    -v "$here/grafana/dashboards:/var/lib/grafana/dashboards:ro" \
    "grafana/grafana:$GRAFANA_VERSION" >/dev/null

  local deadline=$(( SECONDS + 90 ))
  until curl -sf "http://127.0.0.1:$GRAFANA_PORT/api/health" >/dev/null; do
    grafana_running || die "grafana exited during startup. Its log:
$(docker logs --tail 20 "$container" 2>&1)"
    (( SECONDS < deadline )) || die "grafana did not answer within 90s. See: docker logs $container"
    sleep 1
  done
}

up() {
  tunnel_up
  grafana_up
  if ! curl -sf "$prometheus/-/ready" >/dev/null; then
    echo "dashboards: no Prometheus is answering at $prometheus yet. Start it on the fleet host with ops/prometheus.sh up; the panels fill in once it answers." >&2
  fi
  echo "dashboards: $dashboard"
}

down() {
  if command -v docker >/dev/null && docker info >/dev/null 2>&1 && docker rm -f "$container" >/dev/null 2>&1; then
    echo "dashboards: grafana stopped"
  fi
  if tunnel_open; then
    ssh -S "$socket" -O exit "$GPU_HOST" 2>/dev/null
    echo "dashboards: tunnel closed"
  fi
}

status() {
  if [[ -z "$GPU_HOST" ]]; then
    echo "tunnel      none, GPU_HOST is empty"
  elif tunnel_open; then
    echo "tunnel      open to $GPU_HOST"
  else
    echo "tunnel      closed"
  fi
  if curl -sf "$prometheus/-/ready" >/dev/null; then
    echo "prometheus  answering at $prometheus"
  else
    echo "prometheus  not answering at $prometheus"
  fi
  if command -v docker >/dev/null && grafana_running; then
    echo "grafana     $dashboard"
  else
    echo "grafana     stopped"
  fi
}

case "${1:-}" in
  up)     up ;;
  down)   down ;;
  status) status ;;
  *)      die "usage: $0 up|down|status" ;;
esac
