#!/bin/sh
# Proves the web console end to end against the REAL local stack:
#
#   disposable Keycloak (fixture realm) + production `ants-api` binary
#   (memory store, process sandbox, local-git SCM — the same fully-real
#   pipeline as `make demo`) + the built Next.js console.
#
# Nothing here touches a shared environment; every process is torn down
# afterwards. Docker is required for the Keycloak fixture.
#
# Usage: scripts/test-web-e2e.sh [keep]
set -eu

ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
WORK="$ROOT/.local/tranche-3_7/web-e2e"
KC_CONTAINER=ants-test-keycloak
KC_PORT=54331
API_ADDR=127.0.0.1:18080
WEB_PORT=3100
REALM=ants
IMAGE=${ANTS_KEYCLOAK_IMAGE:-quay.io/keycloak/keycloak:26.4.1}

mkdir -p "$WORK"

cleanup() {
  status=$?
  for pid in "$WEB_PID" "$API_PID"; do
    if [ -n "${pid:-}" ]; then kill "$pid" >/dev/null 2>&1 || true; fi
  done
  if [ "${1:-}" != "keep" ]; then
    docker rm -f "$KC_CONTAINER" >/dev/null 2>&1 || true
    rm -rf "$WORK"
  else
    echo "artifacts left: $WORK (keycloak http://127.0.0.1:$KC_PORT/realms/$REALM)"
  fi
  exit $status
}
trap 'cleanup "${KEEP:-}"' EXIT INT TERM

if [ "${1:-}" = "keep" ]; then KEEP=keep; shift; fi

command -v docker >/dev/null || { echo "docker is required"; exit 1; }

# --- disposable identity provider -------------------------------------------
docker rm -f "$KC_CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$KC_CONTAINER" \
  -p "127.0.0.1:$KC_PORT":8080 \
  -v "$ROOT/scripts/keycloak/realm-ants.json":/opt/keycloak/data/import/realm-ants.json:ro \
  -e KEYCLOAK_ADMIN=fixture-admin -e KEYCLOAK_ADMIN_PASSWORD=fixture-admin-password \
  "$IMAGE" start-dev --import-realm >/dev/null
echo "waiting for keycloak ($IMAGE)..."
i=0
until curl -sf "http://127.0.0.1:$KC_PORT/realms/$REALM/.well-known/openid-configuration" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 120 ]; then
    docker logs "$KC_CONTAINER" 2>&1 | tail -20
    exit 1
  fi
  sleep 1
done

# --- API server (production binary, real execution drivers) ------------------
(cd "$ROOT" && go build -o "$WORK/ants-api" ./cmd/api)
cat > "$WORK/api.yaml" <<YAML
server:
  http_addr: "$API_ADDR"
auth:
  oidc:
    issuer_url: "http://127.0.0.1:$KC_PORT/realms/$REALM"
    audience: "ants-api"
    tenant_claim: "ants_tenant"
store:
  mode: memory
sandbox:
  driver: process
scm:
  driver: local_git
policy:
  allow_local_commits: true
log:
  level: info
YAML
"$WORK/ants-api" serve --config "$WORK/api.yaml" >"$WORK/api.log" 2>&1 &
API_PID=$!
i=0
until curl -sf "http://$API_ADDR/readyz" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 120 ] || ! kill -0 "$API_PID" 2>/dev/null; then
    tail -20 "$WORK/api.log"
    exit 1
  fi
  sleep 1
done

# --- web console (production build; config is read at server runtime) -------
cd "$ROOT/apps/web"
SESSION_KEY="$(openssl rand -base64 32)"
export ANTS_WEB_URL="http://127.0.0.1:$WEB_PORT" \
ANTS_API_BASE_URL="http://$API_ADDR" \
ANTS_OIDC_ISSUER_URL="http://127.0.0.1:$KC_PORT/realms/$REALM" \
ANTS_OIDC_CLIENT_ID="ants-web" \
ANTS_SESSION_KEY="$SESSION_KEY"
pnpm exec next build >"$WORK/web-build.log" 2>&1 || { tail -30 "$WORK/web-build.log"; exit 1; }

pnpm exec next start -p "$WEB_PORT" >"$WORK/web.log" 2>&1 &
WEB_PID=$!
i=0
until curl -sf "http://127.0.0.1:$WEB_PORT/login" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 60 ] || ! kill -0 "$WEB_PID" 2>/dev/null; then
    tail -20 "$WORK/web.log"
    exit 1
  fi
  sleep 1
done

# --- browser proof ------------------------------------------------------------
export ANTS_E2E_ISSUER="http://127.0.0.1:$KC_PORT/realms/$REALM"
status=0
pnpm exec playwright test "$@" || status=$?

if [ "$status" -ne 0 ]; then
  echo "--- console server log tail (diagnostics) ---"
  tail -40 "$WORK/web.log" 2>/dev/null || true
fi

exit "$status"
