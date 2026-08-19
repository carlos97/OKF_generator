#!/usr/bin/env bash
# Comprueba que una cancelacion cooperativa durante una conversion en vuelo no
# resucita el trabajo ni publica un bundle. Requiere Docker Compose, curl y jq.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

api="${OKF_API:-http://localhost:8080/api/v1}"
file="$root/testdata/01-breve.md"
job_id=""

restore_worker() {
  docker compose up -d --no-deps --force-recreate worker >/dev/null || true
}
trap restore_worker EXIT

fail() { echo "[FALLA] $*" >&2; exit 1; }

echo '=== Cancelacion cooperativa sin resurreccion ==='
command -v jq >/dev/null || fail 'jq es requerido'
docker compose up -d frontend >/dev/null
env DEMO_SLOW_MODE_MS=15000 JOB_TIMEOUT=20s JOB_LEASE=25s SWEEPER_INTERVAL=2s \
  docker compose up -d --no-deps --force-recreate worker >/dev/null

deadline=$((SECONDS + 90))
until [[ "$(curl -sS -o /dev/null -w '%{http_code}' "$api/jobs" || true)" == 401 ]]; do
  (( SECONDS < deadline )) || fail 'la API no quedo disponible'
  sleep 1
done

token="$(curl -fsS -X POST "$api/auth/login" -H 'Content-Type: application/json' \
  --data '{"email":"ana@demo.local","password":"Demo12345"}' | jq -er '.token')"
job_id="$(curl -fsS -X POST "$api/documents" -H "Authorization: Bearer $token" \
  -F "file=@$file" | jq -er '.job_id')"

deadline=$((SECONDS + 30))
while (( SECONDS < deadline )); do
  status="$(curl -fsS "$api/jobs/$job_id" -H "Authorization: Bearer $token" | jq -r '.status')"
  [[ "$status" == running ]] && break
  sleep 1
done
[[ "${status:-}" == running ]] || fail "no llego a running: ${status:-desconocido}"

curl -fsS -X POST "$api/jobs/$job_id/cancel" -H "Authorization: Bearer $token" >/dev/null
deadline=$((SECONDS + 45))
while (( SECONDS < deadline )); do
  status="$(curl -fsS "$api/jobs/$job_id" -H "Authorization: Bearer $token" | jq -r '.status')"
  [[ "$status" == canceled ]] && break
  [[ "$status" == queued || "$status" == succeeded ]] && fail "la cancelacion revivio/publico el trabajo: $status"
  sleep 1
done
[[ "${status:-}" == canceled ]] || fail "no termino canceled: ${status:-desconocido}"

evidence="$(docker compose exec -T postgres psql -U okf -d okf -At -F '|' -c \
  "SELECT j.status, j.attempt, count(b.id) FROM jobs j LEFT JOIN bundles b ON b.job_id=j.id WHERE j.id='$job_id' GROUP BY j.id;")"
IFS='|' read -r db_status attempt bundles <<<"$evidence"
[[ "$db_status" == canceled && "$attempt" == 1 && "$bundles" == 0 ]] || fail "evidencia invalida: $evidence"
echo '  [OK] cancelado durante ejecucion; sin reencolar ni publicar bundle.'
