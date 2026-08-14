# syntax=docker/dockerfile:1
# ---------------------------------------------------------------------------
# Imagen del backend. Un solo Dockerfile, un target por binario.
#
# La version de Go debe IGUALAR o superar la del host (1.26): si go.mod declara
# `go 1.26` y la imagen trae 1.25, el build falla con "go.mod requires go >=
# 1.26". GOTOOLCHAIN=local ademas impide que el build descargue silenciosamente
# otra toolchain, lo que romperia la reproducibilidad que se reclama.
# ---------------------------------------------------------------------------

# --- dependencias: capa cacheada que no se repite al cambiar codigo ---------
FROM golang:1.26-alpine AS deps
ENV GOTOOLCHAIN=local
ENV GOFLAGS=-mod=readonly
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download

# --- compilacion ------------------------------------------------------------
FROM deps AS build
ARG CMD=api
ARG VERSION=dev
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/app ./cmd/${CMD}

# --- tests: se ejecuta explicitamente, no es decoracion ---------------------
# Uso:  docker compose build --target test api
FROM deps AS test
COPY . .
RUN go vet ./... && go test ./... -count=1

# --- runtime de la API: distroless, sin shell y sin usuario root ------------
FROM gcr.io/distroless/static-debian12:nonroot AS api
COPY --from=build /out/app /app
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/app"]

# --- runtime del worker -----------------------------------------------------
# Se usa debian-slim y no distroless porque el soporte opcional de PDF necesita
# poppler-utils (pdftohtml), que aporta el tamano de fuente por span y es lo
# unico que hace viable inferir la jerarquia de un PDF. La API no lo necesita y
# se queda en distroless: cada imagen dimensionada a su funcion.
FROM debian:12-slim AS worker
RUN apt-get update \
 && apt-get install -y --no-install-recommends poppler-utils ca-certificates \
 && rm -rf /var/lib/apt/lists/*
RUN useradd --uid 65532 --create-home --shell /usr/sbin/nologin okf
COPY --from=build /out/app /app
USER 65532:65532
ENTRYPOINT ["/app"]

# --- runtime de las utilidades (migraciones, buckets, topologia, seed) ------
FROM debian:12-slim AS tools
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates \
 && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/app /app
ENTRYPOINT ["/app"]
