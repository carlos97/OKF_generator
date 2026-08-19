#!/usr/bin/env bash
# Comprueba que un worker muerto a mitad de una conversion no deja el trabajo
# huerfano ni publica dos bundles. Requiere Docker Compose, curl y jq.
#
# Uso desde la raiz del repositorio:
#   ./scripts/verify-worker-crash-recovery.sh
#
# No borra volumenes ni datos. Reinicia unicamente el servicio worker y, al
# salir, lo recrea con la configuracion normal de docker-compose.yml.

set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

api="${OKF_API:-http://localhost:8080/api/v1}"
file="$root/testdata/01-breve.md"
job_id=""
restore_worker() {
  # Las variables de prueba solo viven en el proceso del comando que las usó.
  # Esta recreacion deja el worker con los defaults declarados en Compose.
  docker compose up -d --no-deps --force-recreate worker >/dev/null || true
}
trap restore_worker EXIT

fail() {
  echo "[FALLA] $*" >&2
  exit 1
}

wait_api() {
  local deadline=$((SECONDS + 90)) code
  while (( SECONDS < deadline )); do
    code="$(curl -sS -o /dev/null -w '%{http_code}' --max-time 5 "$api/jobs" || true)"
    [[ "$code" == "401" ]] && return
    sleep 1
  done
  fail "la API no respondio como API autenticada (ultimo codigo: ${code:-sin respuesta})"
}

job_json() {
  curl -fsS "$api/jobs/$job_id" -H "Authorization: Bearer $token"
}

echo '=== Recuperacion de worker tras SIGKILL ==='
[[ -f "$file" ]] || fail "no existe el documento de prueba: $file"
command -v jq >/dev/null || fail 'jq es requerido'

# La API se prueba a traves del proxy publico. Asegurarlo aqui evita depender
# de que una ejecucion anterior de Compose haya dejado frontend iniciado.
docker compose up -d frontend >/dev/null

# El modo lento abre una ventana estable para matar el contenedor tras el claim.
# Los tiempos respetan la invariante JOB_LEASE > JOB_TIMEOUT del worker.
env DEMO_SLOW_MODE_MS=15000 JOB_TIMEOUT=20s JOB_LEASE=25s SWEEPER_INTERVAL=2s \
  docker compose up -d --no-deps --force-recreate worker >/dev/null

wait_api
token="$(curl -fsS -X POST "$api/auth/login" -H 'Content-Type: application/json' \
  --data '{"email":"ana@demo.local","password":"Demo12345"}' | jq -er '.token')"

upload="$(curl -fsS -X POST "$api/documents" -H "Authorization: Bearer $token" \
  -F "file=@$file")"
job_id="$(jq -er '.job_id' <<<"$upload")"
echo "  trabajo aceptado: $job_id"

deadline=$((SECONDS + 30))
while (( SECONDS < deadline )); do
  status="$(job_json | jq -er '.status')"
  [[ "$status" == 'running' ]] && break
  sleep 1
done
[[ "${status:-}" == 'running' ]] || fail "el worker no reclamo el trabajo antes del timeout (estado: ${status:-desconocido})"

docker compose kill -s SIGKILL worker >/dev/null
env DEMO_SLOW_MODE_MS=15000 JOB_TIMEOUT=20s JOB_LEASE=25s SWEEPER_INTERVAL=2s \
  docker compose up -d --no-deps --force-recreate worker >/dev/null
echo '  worker detenido con SIGKILL despues del claim; esperando recuperar el lease...'

deadline=$((SECONDS + 150))
while (( SECONDS < deadline )); do
  status="$(job_json | jq -er '.status')"
  [[ "$status" == 'succeeded' ]] && break
  [[ "$status" =~ ^(invalid|failed|dead|canceled)$ ]] && fail "el trabajo termino en $status"
  sleep 2
done
[[ "${status:-}" == 'succeeded' ]] || fail "no se recupero ni publico en 150 s (ultimo estado: ${status:-desconocido})"

# Esta consulta prueba la recuperacion (claim_count >= 2) y C6 con datos
# persistidos, no solo con la respuesta HTTP: una fila publicable y un evento
# de publicacion, ambos unicos para el trabajo.
evidence="$(docker compose exec -T postgres psql -U okf -d okf -At -F '|' -c \
  "SELECT j.status, j.claim_count,
          (SELECT COUNT(*) FROM bundles b
            WHERE b.job_id = j.id AND b.status = 'published'),
          (SELECT COUNT(*) FROM bundles b WHERE b.job_id = j.id),
          (SELECT COUNT(*) FROM job_events e
            WHERE e.job_id = j.id AND e.type = 'published')
     FROM jobs j
    WHERE j.id = '$job_id';")"

IFS='|' read -r db_status claims published bundles published_events <<<"$evidence"
[[ "$db_status" == 'succeeded' ]] || fail "Postgres no confirma succeeded: $evidence"
[[ "$claims" -ge 2 ]] || fail "no hay evidencia de reclamacion posterior al crash: $evidence"
[[ "$published" == 1 && "$bundles" == 1 && "$published_events" == 1 ]] ||
  fail "se detecto publicacion no unica: $evidence"

echo "  [OK] recuperado con $claims claims; 1 bundle publicado y 1 evento published"
echo 'La recuperacion tras crash y la ausencia de bundle duplicado pasan.'
