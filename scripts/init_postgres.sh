#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
dsn="${LINDESK_DATABASE_DSN:-postgres://lindesk:lindesk@localhost:5432/lindesk?sslmode=disable}"

run_sql_file() {
  local sql_file="$1"

  if command -v psql >/dev/null 2>&1; then
    psql "$dsn" -v ON_ERROR_STOP=1 -f "$sql_file"
    return
  fi

  if command -v docker >/dev/null 2>&1; then
    docker compose exec -T postgres psql "$dsn" -v ON_ERROR_STOP=1 <"$sql_file"
    return
  fi

  echo "psql is required. Install PostgreSQL client tools or start the compose postgres service." >&2
  exit 127
}

if [[ "${1:-}" == "--reset" ]]; then
  run_sql_file "$repo_root/migrations/000002_seed_demo_data.down.sql"
  run_sql_file "$repo_root/migrations/000001_create_core_schema.down.sql"
fi

run_sql_file "$repo_root/migrations/000001_create_core_schema.up.sql"
run_sql_file "$repo_root/migrations/000002_seed_demo_data.up.sql"

echo "PostgreSQL schema and demo data are ready."
