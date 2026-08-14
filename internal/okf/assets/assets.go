// Package assets extrae los recursos embebidos del documento a assets/.
package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"

	"github.com/uniandes-isis4426/okfp/internal/okf/bundlefs"
	"github.com/uniandes-isis4426/okfp/internal/okf/docmodel"
)

// Result resume la extraccion.
type Result struct {
	Extracted int
	Skipped   int
	Remote    int
	Notes     []string
}

// Extract recorre los bloques, materializa los recursos embebidos en assets/ y
// reescribe la referencia de cada imagen a una ruta relativa dentro del bundle.
//
// Decisiones:
//
//   - El nombre sale del SHA-256 del contenido mas la extension deducida por
//     sniffing, nunca del nombre que traia el recurso. Eso elimina de raiz el
//     path traversal y el zip-slip (jamas escribimos un nombre venido del
//     usuario), deduplica el caso real del logo repetido en cuarenta paginas y
//     hace el bundle reproducible.
//   - Los recursos REMOTOS no se descargan. El worker vive en la red interna
//     junto a la base de datos, la cola y el almacenamiento; seguir una URL
//     controlada por el usuario lo convertiria en un proxy hacia esos
//     servicios, y validar el host no basta por redirecciones y DNS rebinding.
//     Se conserva la referencia externa y se registra una advertencia.
//   - Solo se materializa un recurso si alguna unidad conserva su referencia:
//     asi no quedan ficheros huerfanos en el bundle.
func Extract(units [][]docmodel.Block, fs *bundlefs.FS, budget int64) Result {
	var res Result
	seen := map[string]string{} // hash -> ruta ya escrita

	for _, blocks := range units {
		for i := range blocks {
			img := blocks[i].Image
			if img == nil {
				continue
			}

			if img.Remote {
				res.Remote++
				continue
			}
			if len(img.Data) == 0 {
				// Referencia relativa a un fichero que no acompana al
				// documento: se conserva tal cual y se advierte.
				if img.URL != "" && !strings.HasPrefix(img.URL, "./assets/") {
					res.Skipped++
				}
				continue
			}
			if int64(len(img.Data)) > budget {
				res.Skipped++
				continue
			}

			sum := sha256.Sum256(img.Data)
			hash := hex.EncodeToString(sum[:])[:16]

			path, ok := seen[hash]
			if !ok {
				ext := extensionFor(img.Data, img.MimeType)
				path = bundlefs.AssetsDir + hash + ext
				fs.Put(path, img.Data)
				seen[hash] = path
				budget -= int64(len(img.Data))
				res.Extracted++
			}

			img.URL = "./" + path
			// Los bytes ya viven en el bundle; liberarlos evita mantener el
			// documento entero duplicado en memoria del worker.
			img.Data = nil
		}
	}

	if res.Remote > 0 {
		res.Notes = append(res.Notes,
			plural(res.Remote,
				"Se conservo %d referencia a un recurso remoto sin descargarla (politica de seguridad del worker)",
				"Se conservaron %d referencias a recursos remotos sin descargarlas (politica de seguridad del worker)"))
	}
	if res.Skipped > 0 {
		res.Notes = append(res.Notes,
			plural(res.Skipped,
				"Se omitio %d recurso por superar el presupuesto de assets o no estar disponible",
				"Se omitieron %d recursos por superar el presupuesto de assets o no estar disponibles"))
	}
	return res
}

// extensionFor deduce la extension de los bytes reales, no del nombre.
func extensionFor(data []byte, declared string) string {
	mt := declared
	if mt == "" || mt == "application/octet-stream" {
		mt = http.DetectContentType(data)
	}
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = mt[:i]
	}
	switch strings.TrimSpace(mt) {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	case "image/svg+xml", "text/xml":
		return ".svg"
	}
	return ".bin"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return sprintf(one, n)
	}
	return sprintf(many, n)
}
