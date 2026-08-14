// Package bundlezip genera el ZIP del bundle por streaming.
//
// Vive FUERA de internal/okf a proposito. Es la unica pieza relacionada con el
// bundle que necesita la API (para servir la descarga), y si estuviera bajo
// internal/okf la comprobacion de capas
//
//	go list -deps ./cmd/api | grep internal/okf
//
// daria un falso positivo y dejaria de servir como garantia de que el binario de
// la API no contiene el conversor. Empaquetar no es convertir.
package bundlezip

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/uniandes-isis4426/okfp/internal/domain"
)

// Source entrega los ficheros del bundle uno a uno.
type Source interface {
	Open(ctx context.Context, path string) (io.ReadCloser, error)
}

// Entry es un fichero del manifiesto.
type Entry struct {
	Path string
	Size int64
}

// fixedModTime hace el ZIP reproducible: dos descargas del mismo bundle
// producen bytes identicos y por tanto el mismo hash, lo que permite
// demostrarlo en camara con dos Get-FileHash. Usar time.Now() lo impediria.
var fixedModTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

// StreamZip escribe el ZIP directamente sobre el destino, copiando cada objeto
// del almacenamiento segun avanza.
//
// El paquete completo NUNCA se materializa en memoria ni en el disco de la API:
// el consumo de memoria es el de un buffer de copia, independientemente de que
// el bundle pese 3 KB o 300 MB.
//
// Contrato de errores: el llamante DEBE haber verificado propiedad y existencia
// antes de invocar esta funcion, porque en cuanto se escribe el primer byte ya
// se ha enviado un 200 OK y no hay forma legitima de cambiar el codigo de
// estado. Si algo falla a mitad, se devuelve el error y el handler aborta la
// conexion: el cliente recibe un ZIP sin directorio central, que cualquier
// herramienta rechaza. Es preferible una corrupcion DETECTABLE a una silenciosa.
func StreamZip(ctx context.Context, w io.Writer, src Source, entries []Entry) error {
	zw := zip.NewWriter(w)

	buf := make([]byte, 256*1024)

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return err
		}

		hdr := &zip.FileHeader{
			Name:     e.Path,
			Method:   methodFor(e.Path),
			Modified: fixedModTime,
		}
		hdr.SetMode(0o644)

		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return fmt.Errorf("crear entrada %s: %w", e.Path, err)
		}

		rc, err := src.Open(ctx, e.Path)
		if err != nil {
			return fmt.Errorf("abrir %s: %w", e.Path, err)
		}
		_, err = io.CopyBuffer(fw, rc, buf)
		rc.Close()
		if err != nil {
			return fmt.Errorf("copiar %s: %w", e.Path, err)
		}
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf("cerrar zip: %w", err)
	}
	return nil
}

// methodFor evita recomprimir lo que ya esta comprimido: gastar CPU de la API
// en volver a comprimir un PNG o un JPEG no reduce bytes. El Markdown, en
// cambio, comprime bien y es la mayor parte del bundle en numero de ficheros.
func methodFor(path string) uint16 {
	switch strings.ToLower(ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".pdf", ".zip", ".mp4", ".woff2":
		return zip.Store
	default:
		return zip.Deflate
	}
}

func ext(p string) string {
	if i := strings.LastIndexByte(p, '.'); i >= 0 {
		return p[i:]
	}
	return ""
}

// WriteHeaders fija las cabeceras de la respuesta de descarga.
//
// No se fija Content-Length: el tamano final no se conoce hasta escribir el
// directorio central, y anunciarlo mal produce un cuerpo mas largo que el
// declarado, con el que el navegador trunca el ZIP y la conexion queda
// envenenada para la siguiente peticion. Chunked es correcto; el coste es
// perder el porcentaje de la barra de progreso, que la UI compensa mostrando el
// tamano esperado desde el manifiesto.
func WriteHeaders(w http.ResponseWriter, filename string) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", filename))
	w.Header().Set("Accept-Ranges", "none")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
}

var _ = domain.ErrNotFound
