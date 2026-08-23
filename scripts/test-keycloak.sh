#!/bin/sh
# Provisions a disposable Keycloak container with the deterministic Ants
# fixture realm (scripts/keycloak/realm-ants.json) and runs every OIDC
# integration test against it, then tears everything down.
#
# The realm is imported from a versioned JSON file, so the fixture needs no
# external account and no manual console steps. Client secrets in it are
# fixture-only values for a loopback-bound throwaway realm.
#
# Usage: scripts/test-keycloak.sh [keep]
set -eu

CONTAINER=ants-test-keycloak
PORT=54331
REALM=ants
IMAGE=${ANTS_KEYCLOAK_IMAGE:-quay.io/keycloak/keycloak:26.4.1}

cleanup() {
  if [ "${1:-}" != "keep" ]; then
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  else
    echo "container $CONTAINER left running (issuer http://127.0.0.1:$PORT/realms/$REALM)"
  fi
}

docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" \
  -p "$PORT":8080 \
  -v "$PWD/scripts/keycloak/realm-ants.json":/opt/keycloak/data/import/realm-ants.json:ro \
  -e KEYCLOAK_ADMIN=fixture-admin -e KEYCLOAK_ADMIN_PASSWORD=fixture-admin-password \
  "$IMAGE" start-dev --import-realm >/dev/null

echo "waiting for keycloak ($IMAGE)..."
i=0
until curl -sf "http://127.0.0.1:$PORT/realms/$REALM/.well-known/openid-configuration" >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 120 ]; then
    docker logs "$CONTAINER" 2>&1 | tail -20
    cleanup
    exit 1
  fi
  sleep 1
done

export TEST_OIDC_ISSUER="http://127.0.0.1:$PORT/realms/$REALM"
status=0
go test -count=1 -race ./internal/authn/... || status=$?

cleanup "${1:-}"
exit "$status"
