#!/bin/sh
# Provisions a disposable PostgreSQL container, runs the migration
# integration tests against it, and tears everything down.
# Usage: scripts/test-postgres.sh [keep]
set -eu

CONTAINER=ants-test-postgres
PORT=54329
DB=ants_migration_test

cleanup() {
  if [ "${1:-}" != "keep" ]; then
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  else
    echo "container $CONTAINER left running for inspection"
  fi
}

docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" \
  -e POSTGRES_USER=ants -e POSTGRES_PASSWORD=ants -e POSTGRES_DB="$DB" \
  -p "$PORT":5432 postgres:16-alpine >/dev/null

echo "waiting for postgres..."
i=0
until docker exec "$CONTAINER" pg_isready -U ants >/dev/null 2>&1; do
  i=$((i + 1))
  if [ "$i" -gt 60 ]; then
    cleanup
    exit 1
  fi
  sleep 0.5
done

export ANTS_TEST_PG_DSN="postgres://ants:ants@127.0.0.1:$PORT/$DB?sslmode=disable"
status=0
go test -count=1 -race ./internal/store/migrate/ || status=$?

cleanup "${1:-}"
exit "$status"
