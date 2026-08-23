# Plataforma de conversión documental a bundles OKF

Proyecto de nivelación · **ISIS4426 Desarrollo de Soluciones Cloud** (Uniandes)

Plataforma web multiusuario que recibe documentos, los convierte **de forma
asíncrona** mediante workers en Go y produce un *bundle* de conocimiento
compatible con Open Knowledge Format: una carpeta autocontenida con `index.md`,
`log.md` y un documento Markdown por unidad lógica detectada.

---

## 1. Arranque en un solo comando

```bash
git clone <repositorio> && cd v1
docker compose up --build
```

Eso es todo. No hace falta copiar ningún `.env`, ni crear buckets, ni aplicar
migraciones a mano: todas las variables tienen valor por defecto en
`docker-compose.yml` y tres servicios de un solo uso (`migrator`, `minio-init`,
`topology`) dejan el sistema operativo por sí mismos.

Cuando termine:

| Servicio | URL | Credenciales |
|---|---|---|
| Aplicación | http://localhost:8080 | `ana@demo.local` / `Demo12345` |
| | | `beto@demo.local` / `Demo12345` |
| Consola de RabbitMQ | http://localhost:15672 | `okf` / `okf_dev_password` |
| Consola de MinIO | http://localhost:9001 | `okfadmin` / `okfadmin_dev_password` |

Se crean **dos** usuarios a propósito: el aislamiento multiusuario solo se puede
demostrar si hay un segundo propietario cuyos recursos intentar leer.

**Requisitos:** Docker 24+ y Docker Compose v2. **No** se necesita Go ni Node en
la máquina: todo se compila en imágenes multietapa.

> **Antes de ejecutar:** compruebe que el motor de Docker está realmente
> operativo, no sólo que la aplicación está abierta. En Windows con el backend
> WSL2, Docker Desktop puede tardar varios minutos en terminar de arrancar y
> durante ese tiempo `docker ps` responde pero `docker pull` se queda colgado sin
> mensaje de error. La comprobación fiable es:
>
> ```powershell
> docker run --rm hello-world
> ```
>
> Si ese comando no termina en unos segundos, el motor no está listo: espere a que
> el icono de Docker Desktop deje de animarse, o reinícielo desde
> *Troubleshoot → Restart*. Lanzar `docker compose up` antes de eso produce una
> construcción que parece progresar y nunca avanza.

> **Volúmenes de una versión anterior:** si el volumen `rabbitdata` ya existía,
> puede contener colas declaradas por una versión previa del código con otros
> argumentos. RabbitMQ responde entonces `PRECONDITION_FAILED (406)` a la
> redeclaración y el servicio `topology` sale con código 1, lo que deja a `api` y
> `worker` sin arrancar («dependency failed to start»). El servicio lo **repara
> solo**: borra la cola incompatible y la vuelve a declarar, y lo anuncia en su
> propia salida.
>
> La reparación exige que la cola esté **vacía**, y no por prudencia genérica: el
> barredor solo republica trabajos cuyo mensaje nunca se confirmó, así que borrar
> una cola con mensajes ya confirmados perdería trabajo que nada recuperaría. Si
> queda algo dentro, `topology` se detiene con la instrucción concreta: esperar a
> que los workers la vacíen y repetir `docker compose up topology`, o descartar su
> contenido a mano con
>
> ```powershell
> docker compose exec rabbitmq rabbitmqctl delete_queue okf.jobs.q
> ```
>
> No hace falta `docker compose down -v`, que además borraría Postgres y MinIO.

---

## 2. Arquitectura

```
navegador
   │  :8080  (ÚNICO puerto publicado)
   ▼
frontend ── nginx: sirve el SPA de Angular en / y hace proxy_pass /api → api:8080
   │        (mismo origen ⇒ cero CORS, y permite escalar la API)
   ▼
api (Go · sin estado · sin volúmenes · N réplicas)
   ├── PutObject del original ──────────► MinIO      (bucket okf-originals)
   ├── INSERT documents + jobs (COMMIT) ► PostgreSQL
   └── Publish + publisher confirm ─────► RabbitMQ   (exchange okf.jobs)
                                              │
                                              ▼
                                worker (Go · N réplicas · sin puertos)
                                   ├── claim CAS + lease ──────► PostgreSQL
                                   ├── lee original / escribe bundle ► MinIO
                                   └── reintentos / cola de fallos ─► RabbitMQ
```

### Elección de cada componente

| Componente | Elección | Por qué | Alternativa descartada |
|---|---|---|---|
| Cola | **RabbitMQ 3.13** | Único candidato que da de fábrica ack manual, reentrega al morir el consumidor, dead-letter exchange declarativo, prefetch y una **consola web** que sirve de evidencia visual en la sustentación. Cliente Go mantenido por el propio equipo de RabbitMQ. | Redis Streams (DLQ artesanal), NATS (menos material de referencia), Kafka (sobredimensionado para tres semanas) |
| Tipo de cola | **classic durable** | Las colas quorum cambian sus valores por defecto entre versiones (en 4.x traen `delivery-limit`), lo que alteraría el comportamiento de reintentos sin tocar el código. El contador de intentos vive en `jobs.attempt`, en la base de datos. | quorum + `x-delivery-limit` |
| Base de datos | **PostgreSQL 16** | Los índices únicos parciales y las transiciones compare-and-swap son el mecanismo que garantiza un único bundle por trabajo. `JSONB` para el informe de validación. | MySQL, MongoDB |
| Almacenamiento | **MinIO** (S3-compatible), cliente `minio-go/v7` | Permite que la API no monte ningún volumen. Se descarta `aws-sdk-go-v2` por dos motivos concretos: su firmante SigV4 no admite cuerpos no *seekable* sobre HTTP sin TLS (justo el caso de una subida en streaming), y sus checksums por defecto corrompen objetos silenciosamente contra MinIO. | `aws-sdk-go-v2`, volumen compartido |
| Frontend | **Angular 21** (línea LTS) | El CLI 22 exige Node ^24.15.0 y el entorno tiene 24.3.0. Standalone + signals + `@if/@for`. | Angular 22 (incompatible) |

### Identidad visual del frontend

La interfaz implementa el documento **SONORA · identidad visual web (v01 · 2026)**.
Lo que se adopta es el **sistema visual**, no la marca: el producto sigue
llamándose OKF, porque es el nombre con el que se entrega y se sustenta.

| Del documento | Dónde vive en el código |
|---|---|
| Paleta (`#0B0D0F` fondo, `#15191C` superficie, `#20262A` elevada, `#43E08B` acento, `#8B7CFF` secundario) | Tokens en [`frontend/src/styles.scss`](frontend/src/styles.scss). Ninguna pantalla escribe un color a mano |
| Regla 60 / 30 / 10 | `--signal` sólo en CTA, estado activo y foco. Nunca como fondo de una zona grande |
| Escala tipográfica (display 32-40, h1 24-28, h2 18-20, cuerpo 14-16, meta 12-13) | Variables `--fs-*`, con `clamp()` en los dos niveles superiores |
| Familia **Inter** | Paquete `@fontsource/inter`, servido **desde la propia imagen** |
| Radios 8-12 px, contenedor 1440 px, gutters 24/16 px | `--radius`, `--radius-sm`, `--container`, `--gutter` |
| Motion 150-250 ms / 250-400 ms | `--t-micro`, `--t-panel`, con `prefers-reduced-motion` respetado |
| Sidebar · main · player | [`app.component.ts`](frontend/src/app/app.component.ts): lateral fija, navegación inferior por debajo de 900 px y **barra de actividad persistente** |
| Search prominente | Buscador del panel, que filtra por nombre, estado visible o identificador |
| Iconografía lineal 20-24 px | [`icon.component.ts`](frontend/src/app/shared/icon.component.ts), SVG en línea con `currentColor` |

Tres consecuencias que conviene conocer antes de tocar nada:

- **La identidad es oscura y no hay variante clara.** El documento fija
  `#0B0D0F` como fondo principal; mantener además una paleta clara duplicaría
  cada decisión y contradiría la dirección creativa. Se declara
  `color-scheme: dark` para que los controles nativos no aparezcan en blanco.
- **La fuente no puede venir de Google Fonts.** La CSP declara
  `font-src 'self' data:`, así que `fonts.gstatic.com` quedaría bloqueado y la
  interfaz caería al tipo del sistema sin ningún aviso. Por eso Inter se empaqueta
  con la aplicación (subconjunto `latin`, cinco pesos, ~140 kB en `woff2`).
- **`optimization.styles.inlineCritical` está en `false`** en
  [`frontend/angular.json`](frontend/angular.json). Con esa optimización activa,
  Angular difiere la hoja de estilos mediante un `onload` **en línea**, y
  `script-src 'self'` lo bloquea: la aplicación se queda con el CSS crítico y sin
  tarjetas, botones ni distintivos. Está anotado también en
  [`frontend/security-headers.conf`](frontend/security-headers.conf), que es donde
  vive la política que lo obliga.

El color **nunca** es el único indicador de estado, como exige el apartado de
accesibilidad: cada distintivo lleva punto y texto, los estados en curso laten,
el elemento activo de la navegación añade una barra al color y el fichero abierto
del visor cambia además de peso.

### La API no mantiene estado

Tres decisiones lo garantizan, y las tres son verificables:

1. El servicio `api` **no tiene `volumes:`** en `docker-compose.yml`.
   ```bash
   docker inspect okfp-api-1 --format '{{json .Mounts}}'   # -> []
   ```
2. La subida usa `r.MultipartReader()` y copia el cuerpo **en streaming** hacia
   el almacenamiento de objetos. No se usa `ParseMultipartForm`, que vuelca a
   disco a partir de cierto tamaño.
3. El binario de la API **ni siquiera contiene el conversor**:
   ```bash
   go list -deps ./cmd/api | grep internal/okf    # sin resultados
   ```
   El test `internal/arch_test.go` lo comprueba en cada ejecución de la batería.

---

## 3. Estructura del repositorio

```
v1/
├── cmd/{api,worker,tools}/     Tres binarios
├── internal/
│   ├── domain/                 Entidades, estados, errores tipados, detección de formato
│   ├── config/                 Configuración por variables discretas
│   ├── app/apiapp/             Casos de uso de la API      (solo cmd/api)
│   ├── app/convert/            Caso de uso del worker      (solo cmd/worker)
│   ├── okf/                    Motor de conversión         (NUNCA importado por la API)
│   │   ├── docmodel/           Representación intermedia común a todos los formatos
│   │   ├── parse/              markdown · text · html · docx · pdf
│   │   ├── segment/            Regla de segmentación en unidades lógicas
│   │   ├── render/             index.md · log.md · conceptos
│   │   ├── assets/             Extracción de recursos embebidos
│   │   └── validate/           Validez de plataforma + conformidad OKF
│   ├── bundlezip/              Empaquetado ZIP por streaming (lo usa la API)
│   └── adapters/               postgres · objectstore · queue · httpapi
├── migrations/                 Esquema SQL embebido con go:embed
├── frontend/                   Angular 21 + nginx
└── testdata/                   Documentos que demuestran cada condición
```

`internal/app` está partido en dos paquetes (`apiapp` y `convert`) porque Go
resuelve dependencias **por paquete y no por fichero**: en un único paquete, el
binario de la API arrastraría el conversor y la garantía anterior sería falsa.

---

## 4. Contrato de la API

Todo bajo `/api/v1`. Los errores tienen **una sola forma** (RFC 7807 extendido
con un código de dominio estable y el identificador de petición):

```json
{ "type": "about:blank", "title": "Recurso no encontrado", "status": 404,
  "code": "not_found", "request_id": "7f3c…" }
```

| Método | Ruta | Notas |
|---|---|---|
| `POST` | `/auth/register`, `/auth/login` | JWT HS256 de 24 h |
| `GET` | `/me` | |
| `GET` | `/meta/limits` | Límites de ingesta, para validar en cliente con los mismos números |
| `POST` | `/documents` | `multipart/form-data`, campo `file`. Devuelve **202** con el `job_id` sin esperar la conversión |
| `GET`/`DELETE` | `/documents`, `/documents/{id}` | El borrado es lógico |
| `GET` | `/documents/{id}/content` | Descarga el documento **original** tal y como se subió, por streaming. Útil para comparar la entrada con el bundle generado |
| `GET` | `/jobs`, `/jobs/{id}` | Estado, veredicto, score OKF, hallazgos y línea de tiempo |
| `POST` | `/jobs/{id}/retry` | Crea un trabajo hijo enlazado al anterior |
| `POST` | `/jobs/{id}/cancel` | 200 si estaba en cola, 202 si es cooperativa |
| `POST` | `/jobs/{id}/replay` | Reinyecta el mensaje (requiere `DEV_TOOLS=true`) |
| `GET` | `/bundles`, `/bundles/{id}` | Manifiesto con tamaños y hashes |
| `GET` | `/bundles/{id}/files/*` | Fichero individual, para el visor |
| `POST` | `/bundles/{id}/download-tickets` | Ticket de un solo uso |
| `GET` | `/bundles/{id}/download?t=…` | ZIP por streaming |
| `GET` | `/healthz`, `/readyz` | |

---

## 5. Cómo probar las seis condiciones verificables

### Comprobación automática

Las seis condiciones están automatizadas. Devuelve código de salida **0** si las
seis pasan, y enumera qué falla si alguna no:

```powershell
$env:DEV_TOOLS="true"; docker compose up -d api    # C6 necesita la reinyección
.\scripts\verify-conditions.ps1
$env:DEV_TOOLS="false"; docker compose up -d api   # dejarlo desactivado
```

Salida esperada (verificada sobre un despliegue limpio):

```
=== C1 · Asincronia efectiva ===
  [OK]   la API devolvio 202 con job_id y estado 'queued' en 127 ms sin esperar la conversion
  [OK]   el trabajo prosiguio sin el cliente y termino en 'succeeded'
...
Las seis condiciones verificables pasan.
```

Para la prueba de C1 hace falta el documento grande, que no se versiona porque
se genera de forma determinista:

```powershell
docker compose -f docker-compose.yml -f docker-compose.dev.yml `
  --profile tools run --rm tools gen-large --sections 400 --out testdata/05-lento-grande.md
```

### Comprobación manual, condición por condición

Los comandos están escritos para **Windows PowerShell 5.1**, que es la consola
por defecto de Windows 10. Tres detalles que importan: hay que escribir
`curl.exe` (a secas, `curl` es un alias de `Invoke-WebRequest` y no acepta `-F`
ni `-w`); no se puede usar `Invoke-RestMethod -Form`, que no existe antes de
PowerShell 6.2; y en los cuerpos JSON las comillas van escapadas como `\"`,
porque PowerShell 5.1 no las escapa al invocar un ejecutable nativo y la API
recibiría un cuerpo inválido.

```powershell
$API = "http://localhost:8080/api/v1"

# Token de Ana
$ana = (curl.exe -s -X POST "$API/auth/login" -H "Content-Type: application/json" `
        -d '{\"email\":\"ana@demo.local\",\"password\":\"Demo12345\"}' | ConvertFrom-Json).token

# Token de Beto (para la prueba de aislamiento)
$beto = (curl.exe -s -X POST "$API/auth/login" -H "Content-Type: application/json" `
         -d '{\"email\":\"beto@demo.local\",\"password\":\"Demo12345\"}' | ConvertFrom-Json).token
```

### C1 · Asincronía efectiva

```powershell
# Generar el documento grande (~12 MiB, 400 secciones)
docker compose --profile tools run --rm tools go run ./cmd/tools gen-large --sections 400

# La respuesta llega en milisegundos aunque el procesamiento tarde decenas de segundos
curl.exe -o NUL -s -w "tiempo de respuesta: %{time_total}s`n" `
  -X POST "$API/documents" -H "Authorization: Bearer $ana" `
  -F "file=@testdata/05-lento-grande.md"

# El cliente puede desaparecer y el trabajo continúa
docker compose logs -f worker
```

Refuerzo independiente del tiempo: **detener la API** a mitad del procesamiento y
comprobar que el trabajo llega igualmente a `succeeded`.

```powershell
docker compose stop api          # 'stop', no 'kill': con 'kill' la política de reinicio lo revive
docker compose ps                # api en Exited mientras el worker sigue avanzando
docker compose start api
```

### C2 · Documento breve

```powershell
curl.exe -s -X POST "$API/documents" -H "Authorization: Bearer $ana" `
  -F "file=@testdata/01-breve.md" | ConvertFrom-Json
```

El bundle debe contener exactamente `index.md`, `log.md` y `documento.md`, con
veredicto `valid` y **cero advertencias**. `log.md` lo dice de forma explícita:
*«El documento no presenta divisiones estructurales … Este es un resultado
esperado y no constituye una advertencia.»*

### C3 · Documento estructurado, orden preservado

```powershell
curl.exe -s -X POST "$API/documents" -H "Authorization: Bearer $ana" `
  -F "file=@testdata/02-capitulos.md" | ConvertFrom-Json
```

Produce un concepto por unidad, enlazados en orden desde `index.md`. En el visor
del frontend los enlaces del índice navegan de verdad, incluidos los enlaces
internos por ancla que originalmente apuntaban dentro del mismo documento.

### C4 · Bundle incompleto que no se publica

Caso natural, sin manipular nada:

```powershell
curl.exe -s -X POST "$API/documents" -H "Authorization: Bearer $ana" `
  -F "file=@testdata/04-invalido.txt" | ConvertFrom-Json
# El trabajo termina en 'invalid'. No hay bundle, y por tanto no hay descarga.

docker compose exec -T postgres psql -U okf -d okf `
  -c "select count(*) from bundles where job_id = '<JOB_ID>';"    # -> 0
```

Y la condición literal del enunciado (ausencia de `index.md` o `log.md`). El
worker se rearranca con la inyección de fallo, se sube un documento **válido** y
se comprueba que aun así no se publica:

```powershell
$env:OKF_FAULT_INJECT="drop_log"; docker compose up -d worker

curl.exe -s -X POST "$API/documents" -H "Authorization: Bearer $ana" `
  -F "file=@testdata/02-capitulos.md"
# -> el trabajo termina en 'invalid' con el hallazgo:
#    PLT-STR-002  El bundle no contiene log.md en la raiz
#    y bundle_id vacio: no se publicó nada

$env:OKF_FAULT_INJECT=""; docker compose up -d worker   # restaurar
```

Verificado en un despliegue real: con `drop_log` el veredicto es `invalid` con un
único hallazgo `PLT-STR-002`; con `drop_index`, `PLT-STR-001` más la cascada de
conceptos que dejan de estar enlazados. En ambos casos las tres rutas de lectura
del bundle quedan cerradas.

La razón por la que esto es sólido: **un bundle inválido no crea fila en
`bundles`**. Como los tres endpoints de lectura (metadatos, fichero individual y
descarga) resuelven contra esa tabla mediante la misma función, no existe
ninguna ruta lateral por la que descargarlo, ni siquiera fichero a fichero.

### C5 · Aislamiento por propietario

```powershell
# Ana sube un documento y obtiene su bundle
$job = (curl.exe -s -X POST "$API/documents" -H "Authorization: Bearer $ana" `
        -F "file=@testdata/01-breve.md" | ConvertFrom-Json).job_id
Start-Sleep -Seconds 5
$bundle = (curl.exe -s "$API/jobs/$job" -H "Authorization: Bearer $ana" | ConvertFrom-Json).bundle_id

# Beto intenta leerlo conociendo el identificador exacto
curl.exe -i -s "$API/bundles/$bundle" -H "Authorization: Bearer $beto"      # 404
curl.exe -i -s "$API/jobs/$job"       -H "Authorization: Bearer $beto"      # 404
curl.exe -i -s -X POST "$API/bundles/$bundle/download-tickets" -H "Authorization: Bearer $beto"  # 404

# Y un identificador inventado responde EXACTAMENTE lo mismo
curl.exe -i -s "$API/bundles/00000000-0000-0000-0000-000000000000" -H "Authorization: Bearer $beto"
```

Se responde **404 y no 403** deliberadamente: un 403 confirmaría que el recurso
existe, y el enunciado exige denegar «sin revelar información». Las dos
respuestas son indistinguibles.

El mecanismo: `ownerID` va en la **firma de cada función del repositorio**, de
modo que no existe ningún `GetByID(id)` que devuelva el recurso para comparar el
propietario después. Ese patrón —leer y luego comparar— es el que se olvida al
añadir un endpoint nuevo.

### C6 · Ausencia de duplicados ante reentrega

```powershell
# Activar la herramienta de demostración.
# Nota: `docker compose up` NO acepta -e; la variable se toma del entorno del
# shell, que es lo que interpola ${DEV_TOOLS:-false} en docker-compose.yml.
$env:DEV_TOOLS="true"; docker compose up -d api

# Reinyectar dos veces el mismo mensaje
curl.exe -s -X POST "$API/jobs/$job/replay?times=2" -H "Authorization: Bearer $ana"

# Sigue habiendo UN solo bundle
docker compose exec -T postgres psql -U okf -d okf `
  -c "select count(*) from bundles where job_id = '$job';"    # -> 1

# Y la traza registra las entregas descartadas
docker compose exec -T postgres psql -U okf -d okf `
  -c "select type, detail from job_events where job_id = '$job' order by id;"
```

Caso más exigente, el que de verdad importa: matar al worker a mitad del
procesamiento y comprobar que **otro worker retoma el trabajo** al expirar el
arrendamiento y que sigue produciéndose un único bundle.

```powershell
docker compose up -d --scale worker=2
# subir el documento grande, y a mitad:
docker compose kill worker
docker compose up -d --scale worker=2
```

---

## 6. Cómo funciona la idempotencia

Es la parte del diseño con más aristas, así que aquí está completa.

**Orden obligatorio en la API.** El `COMMIT` de la transacción que crea el
documento y el trabajo ocurre **siempre antes** de publicar en la cola.
Publicar primero abre una carrera real: con el worker ocioso el consumo ocurre en
milisegundos, el worker recibiría un `job_id` que aún no existe en la base de
datos y el trabajo se perdería de forma intermitente.

**Reclamación por compare-and-swap.** El worker reclama con:

```sql
UPDATE jobs SET status='running', lease_owner=$2, lease_expires_at=now()+$3::interval,
                claim_count = claim_count + 1
 WHERE id = $1
   AND (status = 'queued' OR (status = 'running' AND lease_expires_at < now()))
RETURNING attempt, claim_count;
```

La segunda condición del `WHERE` es la clave: **un trabajo en ejecución con el
arrendamiento vencido debe poder reclamarse**. Sin ella, un worker que muere a
mitad deja el trabajo en `running` para siempre y la reentrega de RabbitMQ se
descarta como «duplicado», dejando el trabajo huérfano, sin bundle, sin cola de
fallos y sin error visible.

`attempt` **no** se incrementa aquí. Contar reclamaciones como intentos hace que
un worker que muere en bucle agote el presupuesto de reintentos y deje el trabajo
permanentemente irreclamable. El contador de negocio lo mueve sólo la
reprogramación de reintentos.

**Qué hace el worker ante cada entrega:**

| Situación | Acción |
|---|---|
| El `UPDATE` devuelve fila | procesar |
| El estado ya es terminal | **ack** y descartar: duplicado real |
| Está en ejecución con arrendamiento vivo | **ack** y descartar: otro worker lo tiene |
| **La fila no existe** | `nack(requeue=false)` → cola de fallos. Nunca un ack silencioso, que perdería el trabajo sin dejar rastro |

**Publicación: reclamar antes de promover.** Éste es el orden que cierra la
condición *también en el almacenamiento*:

1. Subir el bundle a un prefijo temporal, por intento.
2. **Reclamar la fila del bundle** (`INSERT … ON CONFLICT (job_id) DO NOTHING`).
   Si otro intento ganó, se limpia el temporal y se termina **sin tocar el
   prefijo publicado**.
3. Copiar del temporal al prefijo servible.
4. Marcar publicado y cerrar el trabajo, comprobando filas afectadas.

Si el reclamo se hiciera *después* de copiar, el índice único llegaría tarde:
impediría la segunda fila pero no la segunda escritura, y el usuario podría
descargar un ZIP con la mezcla de dos intentos.

**Toda transacción comprueba `RowsAffected()`.** Nunca se confirma una
transacción que no hizo nada. Si la cancelación gana la carrera contra la
publicación, el `UPDATE` del trabajo afecta 0 filas y la transacción se deshace
sin publicar: arbitra PostgreSQL, no el código de aplicación.

**Reintentos.** `nack` con requeue está prohibido: reencola al instante y produce
un bucle de fuego rápido que satura al worker mientras la dependencia caída se
recupera. En su lugar, el trabajo se republica a una cola de espera con TTL y
dead-letter exchange, que lo devuelve a la cola principal pasados 30 segundos.
Los errores se clasifican en transitorios y permanentes: un documento corrupto no
se reintenta, un MinIO reiniciándose sí. Lo desconocido se trata como transitorio
porque cuesta como máximo tres intentos, mientras que el sesgo contrario
descartaría trabajo recuperable.

---

## 7. Validación y clasificación del resultado

Hay **dos ejes independientes** y se calculan siempre los dos:

| Eje | Qué mide | Efecto |
|---|---|---|
| **Validez de plataforma** (`PLT-*`) | Puerta binaria dura | Un solo hallazgo `ERROR` impide la publicación |
| **Conformidad OKF** (`OKF-*`) | Calidad ponderada, 0-100 y grado A–D | **Nunca** bloquea |

La regla de publicación es única y auditable:
`publicable = (nº de hallazgos con eje=plataforma y severidad=ERROR) == 0`.

Eso produce los tres veredictos que el enunciado exige distinguir, y hay un
documento de prueba para cada uno: `01-breve.md` → **válido**,
`03-con-imagenes.html` → **válido con advertencias**, `04-invalido.txt` →
**inválido**.

Reglas que bloquean la publicación: existe `index.md`; existe `log.md`; existe al
menos un concepto; el bloque de índice delimitado existe y tiene enlaces; todos
los enlaces del índice resuelven; todo concepto está enlazado desde el índice; no
hay `.md` huérfanos en la raíz; el front-matter es correcto; las rutas son
relativas y seguras; el contenido es UTF-8 válido; ningún concepto está vacío.

Dos decisiones que evitan invalidar documentos legítimos:

- Los enlaces que provienen de la **prosa del usuario** son advertencia, no
  error. Un documento que empiece con «ver [la guía](./CONTRIBUTING.md)» no puede
  considerarse un bundle inválido.
- Por eso el índice va **delimitado por centinelas** `<!-- okf:toc -->`. Sin
  ellos, la regla «los enlaces del índice siguen el orden de los conceptos» no
  podría distinguir un enlace de concepto del enlace a `log.md` o de uno que
  venga del texto original, e invalidaría todos los bundles.

**Validación en punto fijo.** Se valida, se escribe el `log.md` definitivo y se
**vuelve a validar sobre los bytes exactos que se van a publicar**. Sin la
segunda pasada, el epílogo del log —que interpola nombres de fichero y mensajes
procedentes del documento del usuario— se publicaría sin haber pasado por las
reglas.

---

## 8. Segmentación en unidades lógicas

La regla está cerrada y cubierta por tests de tabla:

1. Se consideran los encabezados de nivel ≤ 3.
2. El **nivel de corte** es el menor nivel que aparece **dos o más veces**. Si
   ninguno se repite, el menor nivel presente. Si no hay encabezados, no hay
   corte.
3. Todo el contenido anterior al primer corte forma una **unidad de preámbulo**.
4. Los encabezados más profundos quedan **dentro** de su concepto.
5. Una sola unidad ⇒ el fichero se llama `documento.md` y **no se emite ninguna
   advertencia** por ese hecho.

El punto 2 merece explicación: cortar siempre por H1 dejaría en una sola unidad
el documento típico que tiene un título y varios `##` (fallando la condición de
documento estructurado), y cortar siempre por H2 dejaría fuera un documento
organizado sólo con H1. «El nivel más alto que de verdad se repite» resuelve los
dos casos con una única regla y sin ramas especiales.

El punto 3 evita un fallo silencioso: si se cortara sólo por los encabezados, la
introducción que va bajo el `# Título` de un documento normal desaparecería del
bundle sin que nada lo advirtiera.

---

## 9. Formatos de entrada

| Formato | Cómo | Estructura |
|---|---|---|
| **Markdown** | AST de goldmark | Explícita |
| **Texto plano** | Heurísticas: subrayados `===`, numeración jerárquica, líneas cortas en mayúsculas | Inferida |
| **HTML** | `x/net/html`, lista blanca de elementos | Explícita |
| **DOCX** | `archive/zip` + `encoding/xml` en streaming; nivel por `w:outlineLvl` → nombre canónico de `styles.xml` → `styleId` normalizado | Explícita |
| **PDF** | `pdftohtml -xml` de poppler-utils; jerarquía inferida del tamaño de fuente | Inferida, con advertencia |

Dos detalles que hacen que esto funcione de verdad:

- El HTML **extrae los recursos `data:` antes de sanear**. Cualquier saneador
  elimina esos atributos, así que hacerlo al revés perdería las imágenes sin que
  nada lo advirtiera.
- Word en español genera `w:styleId="Ttulo1"` (la vocal acentuada se pierde al
  construir el identificador). El nombre se normaliza con NFKD antes de
  compararlo, o un informe de 40 páginas caería al camino sin encabezados y
  produciría una sola unidad.

Los formatos con estructura **inferida** marcan el documento como tal, lo que
justifica el veredicto «válido con advertencias» sin fingir la misma confianza
que un formato con marcado explícito.

**Recursos remotos:** no se descargan nunca. El worker vive en la red interna
junto a la base de datos, la cola y el almacenamiento; seguir una URL controlada
por el usuario lo convertiría en un proxy hacia esos servicios, y validar el
host no basta por redirecciones y *DNS rebinding*. La referencia se conserva con
una advertencia.

---

## 10. Descarga por streaming

El ZIP se genera **al vuelo** sobre el `http.ResponseWriter`, copiando cada
objeto del almacenamiento según avanza. El consumo de memoria de la API es el de
un buffer de copia, sea el bundle de 3 KiB o de 300 MiB.

Tres detalles que lo hacen correcto:

- **Toda** la autorización y la comprobación de existencia ocurren antes de
  escribir el primer byte: una vez escrito, ya se envió `200 OK` y no hay forma
  legítima de responder 403 o 404.
- **No** se fija `Content-Length`: el tamaño final no se conoce hasta escribir el
  directorio central, y anunciarlo mal produce un cuerpo más largo que el
  declarado, con el que el navegador trunca el archivo.
- Si falla a mitad se aborta la conexión: el cliente recibe un ZIP sin directorio
  central, que cualquier herramienta rechaza. Una corrupción **detectable** es
  preferible a una silenciosa.

En nginx, `proxy_buffering off` en esa ruta es imprescindible: con el valor por
defecto acumularía el ZIP completo antes de enviarlo y anularía todo el esfuerzo
en el último salto.

El ZIP es **reproducible**: orden canónico de entradas y fecha fija, así que dos
descargas del mismo bundle producen el mismo hash.

---

## 11. Escalado

```bash
docker compose up -d --scale worker=3     # workers, sin tocar la API
docker compose up -d --scale api=3        # la API también escala
```

Los workers escalan porque no publican puertos y consumen con `prefetch=1`: cada
uno toma un mensaje y no acapara la cola, de modo que el reparto lo hace la
propia cola sin ninguna coordinación adicional.

La API escala porque **no publica puertos**: sólo lo hace `frontend`, y nginx
reparte entre las réplicas por el DNS interno de Compose. Con un puerto publicado
en la API, la segunda réplica fallaría con *port is already allocated*.

En la consola de RabbitMQ se ve el número de consumidores subir y la profundidad
de la cola bajar.

### Por qué nginx resuelve el DNS en cada petición

En [`frontend/nginx.conf`](frontend/nginx.conf) el destino del proxy va en una
**variable** (`set $api_backend ...; proxy_pass $api_backend;`) junto con un
`resolver 127.0.0.11`. No es un detalle de estilo:

Con `proxy_pass http://api:8080` literal, nginx resuelve el nombre **una sola vez**
al cargar la configuración y cachea la IP indefinidamente. En cuanto el contenedor
de la API se recrea —`docker compose up -d api`, un reinicio, un `--scale`— recibe
una IP nueva, nginx sigue enviando tráfico a la vieja y **toda la aplicación
responde 502** hasta reiniciar el frontend. El síntoma es engañoso: `docker compose
ps` muestra todo sano y la API registra «API escuchando».

Con la variable, nginx resuelve por petición respetando el TTL y las réplicas
nuevas entran en rotación solas. Comprobación del reparto, escalando a tres
réplicas y enviando 31 peticiones:

```
okfp-api-1: 12 peticiones
okfp-api-2: 10 peticiones
okfp-api-3:  9 peticiones
```

Se cuenta así:

```bash
docker compose up -d --scale api=3
for i in $(seq 1 30); do curl -s -o /dev/null http://localhost:8080/api/v1/jobs; done
for c in $(docker compose ps -q api); do
  echo "$(docker inspect $c --format '{{.Name}}'): $(docker logs --since 3m $c 2>&1 | grep -c '"path":"/api/v1/jobs"')"
done
```

---

## 12. Variables de configuración

Todas tienen valor por defecto en `docker-compose.yml`; `.env` es **opcional**
(véase `.env.example` para el listado completo y comentado). Las más relevantes:

| Variable | Defecto | Para qué |
|---|---|---|
| `EDGE_PORT` | `8080` | Puerto público. Cámbielo si está ocupado |
| `MAX_UPLOAD_BYTES` | 20 MiB | Límite de subida. Se rechaza con 413, nunca se trunca |
| `PARSE_MAX_BYTES` | 5 MiB | Tope del parseo (acota el peor caso cuadrático) |
| `JOB_TIMEOUT` | `120s` | Presupuesto de conversión por trabajo |
| `JOB_LEASE` | `180s` | Debe ser **mayor** que `JOB_TIMEOUT`; se valida al arrancar |
| `MAX_ATTEMPTS` | `3` | Intentos antes de la cola de fallos |
| `DEV_TOOLS` | `false` | Habilita `/jobs/{id}/replay` para la demostración |
| `DEMO_SLOW_MODE_MS` | `0` | Retardo explícito. Ver la nota de honestidad abajo |
| `OKF_FAULT_INJECT` | vacío | `drop_index` / `drop_log` para demostrar la validación |
| `ENABLE_PDF` | `true` | Requiere poppler en la imagen del worker |

Las credenciales se pasan como variables **discretas** y no como cadenas de
conexión: una contraseña con `@`, `/`, `:` o `#` dentro de una URL rompe el
parseo sin *percent-encoding*, y el síntoma aparece justo cuando se hace lo que
el README pide. La conexión se construye en Go, que se encarga del escapado.

> Si cambia `POSTGRES_PASSWORD` después del primer arranque hace falta
> `docker compose down -v`: el volumen conserva la contraseña de inicialización.

---

## 13. Desarrollo y pruebas

```bash
# Tests en contenedor (los que cuentan: verifican que el repo compila sin el host)
docker compose --profile tools run --rm tools go test ./... -race

# En el host, si tiene Go 1.26
go test ./... -race
go vet ./...
```

No se usa `make`: no está instalado en el entorno de desarrollo y además se rompe
con rutas que contienen espacios. El servicio `tools` de
`docker-compose.dev.yml` cumple la misma función y Compose resuelve las rutas por
su cuenta.

Para desarrollo con recarga:

```bash
docker compose -f docker-compose.yml -f docker-compose.dev.yml up
cd frontend && npm start        # ng serve con proxy a la API
```

El fichero de desarrollo **no** se llama `docker-compose.override.yml` a
propósito: Compose lo cargaría automáticamente y el evaluador acabaría ejecutando
la configuración de desarrollo en lugar de la de entrega.

Pruebas destacadas:

| Test | Qué protege |
|---|---|
| `TestSplit`, `TestNoContentIsLost` | La regla de segmentación, caso por caso |
| `TestC2_DocumentoBreveSinAdvertencias` | Cero advertencias con una sola unidad |
| `TestC3_DocumentoEstructuradoEnOrden` | Un concepto por unidad, enlazados en orden |
| `TestC4_BundleIncompletoNoEsPublicable` | Falta `index.md` o `log.md` ⇒ no publicable |
| `TestDeterminismo` | Dos conversiones producen el mismo contenido |
| `TestTestdata` | Cada documento de demostración da el veredicto prometido |
| `TestCapas` | La API no contiene el conversor; el worker no contiene HTTP |

---

## 14. Limitaciones conocidas

- **PDF es una inferencia, no una lectura estructurada.** Un PDF no declara
  encabezados: la jerarquía se deduce del tamaño de fuente. Es honesto y así se
  refleja en el veredicto, pero no debe esperarse la fidelidad de DOCX o
  Markdown.
- **El token se guarda en `localStorage`.** Una cookie `httpOnly` resistiría
  mejor un XSS, pero exigiría protección CSRF y complicaría el ticket de
  descarga. Se compensa con origen único, CSP estricta (`script-src 'self'`) y
  Markdown renderizado con el HTML embebido desactivado. Es un compromiso
  consciente y no un descuido.
- **Sin métricas Prometheus.** La observabilidad se apoya en la tabla
  `job_events` (traza auditable y consultable desde el frontend) y en logs JSON
  correlacionados por `request_id` y `job_id`. Añadir Prometheus y Grafana
  supondría dos servicios más en el arranque.
- **Paginación por `LIMIT/OFFSET`.** A esta escala es indistinguible de una
  paginación por cursor y elimina una fuente de errores.
- **La cancelación es cooperativa y no instantánea.** Un mensaje ya entregado a
  un worker no puede retirarse de AMQP: el worker atiende la cancelación en su
  siguiente punto de control. El estado `canceling` lo hace visible en la
  interfaz.
- **Los objetos temporales pueden quedar huérfanos** si el worker muere en la
  ventana entre copiar y confirmar. No rompen ninguna condición ni son
  descargables, pero no hay recolector que los limpie.

---

## 15. Declaración del uso de agentes de inteligencia artificial

El enunciado permite el uso abierto de agentes de IA y exige declararlo.

**Se usó Claude (Anthropic) de forma intensiva** en las siguientes tareas:

- **Implementación.** Generación del código Go, del frontend Angular y de la
  orquestación Docker.
- **Documentación.** Este README y los comentarios del código.

**Lo que se revisó y verificó a mano:** la ejecución real de la batería de
pruebas, el despliegue completo desde cero, el comportamiento de las seis
condiciones verificables sobre el sistema en marcha, y las decisiones de diseño
donde el agente propuso alternativas.

**Errores del agente que hubo que corregir durante la implementación**, a modo de
reflexión honesta: propuso una etiqueta de imagen de RabbitMQ que no existe;
situó el empaquetador ZIP dentro del árbol del conversor, lo que rompía la
garantía de capas que el propio diseño reclamaba; y dio por bueno un documento de
prueba «inválido» que en realidad se publicaba sin problemas. Los dos últimos
sólo se descubrieron al ejecutar de verdad la comprobación de capas y los tests,
que es exactamente el motivo por el que existen.

---

## 16. Autores

Carlos Andrés López ·
Universidad de los Andes.
