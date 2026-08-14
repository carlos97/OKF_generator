# syntax=docker/dockerfile:1
# ---------------------------------------------------------------------------
# Imagen del frontend: compila el SPA y lo sirve con nginx, que ademas hace de
# proxy inverso hacia la API (mismo origen => cero CORS).
#
# El host no necesita Node ni Angular CLI instalados: todo ocurre aqui.
# ---------------------------------------------------------------------------

# node:22.12 y no node:22-alpine sin fijar: el CLI de Angular 21 exige
# >=22.12 y una etiqueta flotante puede servir un 22.x anterior, con lo que el
# build falla por engines.
FROM node:22.12-alpine AS build
WORKDIR /app

# Capa de dependencias cacheada aparte del codigo.
COPY package.json package-lock.json* ./
# npm ci exige package-lock.json; si no existe todavia se cae a npm install para
# que el primer build funcione sin haber ejecutado npm en el host.
RUN if [ -f package-lock.json ]; then npm ci; else npm install; fi

COPY . .
ARG VERSION=dev
RUN npm run build -- --configuration production

# --- runtime ----------------------------------------------------------------
FROM nginx:1.27-alpine AS runtime

COPY nginx.conf            /etc/nginx/conf.d/default.conf
COPY security-headers.conf /etc/nginx/security-headers.conf
COPY --from=build /app/dist/browser /usr/share/nginx/html

# nginx escucha en 8080 para poder correr sin privilegios.
RUN sed -i 's|listen       80;|listen 8080;|' /etc/nginx/nginx.conf 2>/dev/null || true \
 && sed -i '/user  nginx;/d' /etc/nginx/nginx.conf \
 && sed -i 's|/var/run/nginx.pid|/tmp/nginx.pid|' /etc/nginx/nginx.conf \
 && sed -i 's|pid        /run/nginx.pid;|pid /tmp/nginx.pid;|' /etc/nginx/nginx.conf \
 && mkdir -p /tmp/nginx-cache \
 && chown -R nginx:nginx /usr/share/nginx/html /var/cache/nginx /tmp/nginx-cache

USER nginx
EXPOSE 8080

HEALTHCHECK --interval=10s --timeout=5s --start-period=10s --retries=5 \
  CMD wget -q -O /dev/null http://127.0.0.1:8080/ || exit 1

CMD ["nginx", "-g", "daemon off;"]
