# Manual de la plataforma de conversion documental

Esta introduccion aparece antes del primer capitulo y por tanto no pertenece a
ninguno de ellos. La plataforma debe conservarla como una unidad de preambulo:
descartarla seria perder contenido del documento de origen en silencio, que es
precisamente el fallo que la regla de segmentacion evita.

Consulte tambien la [seccion de instalacion](#instalacion), cuyo enlace interno
debe seguir funcionando despues de partir el documento en varios ficheros.

## Requisitos previos

Para desplegar la plataforma solo hacen falta Docker y Docker Compose. No se
necesita ni Go ni Node instalados en la maquina: las imagenes se construyen en
contenedores multietapa.

- Docker 24 o superior
- Docker Compose v2
- 4 GiB de memoria disponibles para los contenedores

## Instalacion

El despliegue completo se realiza con una sola invocacion:

```bash
docker compose up --build
```

Ese comando levanta la base de datos, la cola de mensajes, el almacenamiento de
objetos, la API, los workers y el frontend, y ejecuta por si mismo las
migraciones, la creacion de buckets y la declaracion de la topologia de colas.

### Verificacion del despliegue

Una vez arrancado, la interfaz queda disponible en el puerto configurado y la
consola de la cola de mensajes permite observar el flujo de trabajos en vivo.

## Uso diario

El flujo habitual consta de cuatro pasos:

1. Iniciar sesion con una cuenta propia.
2. Arrastrar un documento a la zona de carga.
3. Seguir el estado del trabajo, que avanza solo.
4. Descargar el bundle cuando el resultado sea valido.

La respuesta a la carga es inmediata y devuelve un identificador de trabajo: la
conversion ocurre despues, en un worker independiente.

## Escalado de los workers

Los workers se escalan sin tocar la API, porque el reparto de trabajo lo hace la
propia cola mediante consumo competitivo:

```bash
docker compose up -d --scale worker=3
```

Cada worker toma un mensaje a la vez, de modo que el trabajo se distribuye sin
ninguna coordinacion adicional entre replicas.

## Resolucion de problemas

Si un trabajo queda en cola mas tiempo del esperado, conviene revisar la consola
de la cola de mensajes y los registros del worker. Un trabajo cuyo mensaje se
perdio se recupera automaticamente, y uno cuyo worker murio a mitad se vuelve a
tomar cuando expira su arrendamiento.
