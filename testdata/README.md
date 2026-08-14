# Documentos de prueba

Cada fichero de esta carpeta existe para demostrar una condicion concreta. El
test `internal/okf/testdata_test.go` los convierte y comprueba que el veredicto
es el que aqui se afirma, de modo que ninguna de estas promesas puede quedarse
obsoleta sin que falle la bateria de pruebas.

| Fichero | Demuestra | Resultado esperado |
|---|---|---|
| `01-breve.md` | **Documento breve** | `index.md`, `log.md` y **un unico** `documento.md`. Veredicto **valido** y **cero advertencias**: una sola unidad no es una anomalia. |
| `02-capitulos.md` | **Documento estructurado** | Un concepto por unidad (preambulo + 5 capitulos), enlazados **en orden** desde el indice. Incluye un enlace interno `#instalacion` que debe seguir resolviendo tras partir el documento. |
| `03-con-imagenes.html` | Segundo formato, `assets/` y **veredicto intermedio** | Conceptos por `<h2>`, la imagen embebida extraida a `assets/` nombrada por hash, el `<script>` y el `javascript:` eliminados. Veredicto **valido con advertencias**, porque referencia a proposito una imagen (`no-existe.png`) que no acompana al documento. |
| `04-invalido.txt` | **Bundle que no se publica** | Contiene bytes que no forman UTF-8 valido. Veredicto **invalido** por causa natural: no se crea fila de bundle, no hay descarga posible. |
| `05-lento-grande.md` | **Asincronia efectiva** | No se versiona (pesa varios MB). Se genera con el comando de abajo. |

## Generar el documento grande

```bash
docker compose --profile tools run --rm tools go run ./cmd/tools gen-large --sections 400
```

O, si tiene Go en el host:

```bash
go run ./cmd/tools gen-large --sections 400
```

El fichero resultante ronda los 12 MiB y ~400 secciones. La lentitud del
procesamiento es **emergente**: parsear varios MB, segmentar cientos de unidades,
renderizar cientos de ficheros Markdown, subirlos al almacenamiento, validar
todos los enlaces del indice y empaquetar. Es trabajo real y medible, y el
`log.md` del bundle enumera cada unidad y cada operacion, de modo que la
pregunta "esto es un sleep?" tiene una respuesta comprobable en pantalla.

El generador usa un vocabulario fijo y ninguna fuente de aleatoriedad, asi que
dos ejecuciones producen exactamente el mismo documento.

## Sobre el retardo de demostracion

Existe una variable `DEMO_SLOW_MODE_MS`, **desactivada por defecto (0)**. No es
la evidencia de asincronia; es una red de seguridad para el caso de que la
maquina donde se grabe el video sea tan rapida que el documento grande se procese
en pocos segundos. Si se activa:

- el worker registra un evento `demo_slow_mode` en la linea de tiempo del trabajo,
- `log.md` incluye una nota explicita indicando los milisegundos aplicados.

Nunca es un retardo oculto.

## Sobre la inyeccion de fallo

La condicion "ante la ausencia de `index.md` o `log.md` la validacion falla" se
refiere a un bundle incompleto, y un bundle generado correctamente nunca le falta
un fichero obligatorio: hay que provocarlo. Para demostrarlo en vivo:

```bash
docker compose up -d --force-recreate -e OKF_FAULT_INJECT=drop_log worker
```

El worker elimina `log.md` justo antes de la validacion definitiva. Queda
registrado como una etapa `fault_inject` en la cronologia, asi que la
manipulacion es visible y no se presenta como un fallo espontaneo. La evidencia
principal de validacion sigue siendo `04-invalido.txt`, que falla por una causa
real, y los tests `TestC4_BundleIncompletoNoEsPublicable` cubren literalmente la
ausencia de cada uno de los dos ficheros.
