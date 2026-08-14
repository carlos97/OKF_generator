package domain

import (
	"bytes"
	"mime"
	"net/http"
	"path/filepath"
	"strings"
)

// Format es el formato de entrada detectado.
type Format string

const (
	FormatMarkdown Format = "markdown"
	FormatHTML     Format = "html"
	FormatText     Format = "text"
	FormatDOCX     Format = "docx"
	FormatPDF      Format = "pdf"
	FormatUnknown  Format = "unknown"
)

// SupportedFormats es el catalogo que la API acepta en la subida. PDF se
// habilita solo si la imagen del worker incluye poppler-utils.
var SupportedFormats = []Format{FormatMarkdown, FormatHTML, FormatText, FormatDOCX, FormatPDF}

var extToFormat = map[string]Format{
	".md":       FormatMarkdown,
	".markdown": FormatMarkdown,
	".mdown":    FormatMarkdown,
	".html":     FormatHTML,
	".htm":      FormatHTML,
	".txt":      FormatText,
	".text":     FormatText,
	".docx":     FormatDOCX,
	".pdf":      FormatPDF,
}

// Firmas magicas. La extension y el Content-Type declarado por el cliente son
// datos NO CONFIABLES: el enunciado pide tratar los documentos de entrada como
// tales, asi que la decision final la toman los bytes.
var (
	magicZIP = []byte{0x50, 0x4B, 0x03, 0x04} // PK\x03\x04 -> DOCX (y EPUB, ODT...)
	magicPDF = []byte("%PDF-")
)

// DetectResult es el veredicto de la deteccion de formato.
type DetectResult struct {
	Format    Format
	MediaType string
	// EsZIP indica que los bytes son un contenedor ZIP. Saberlo aqui evita que
	// cada parser tenga que abrir el archivo por su cuenta solo para descartar.
	IsZIP bool
}

// DetectFormat decide el formato a partir de los primeros bytes y del nombre de
// fichero, en ese orden de prioridad.
//
// Dos trampas concretas que este codigo evita:
//
//   - http.DetectContentType NUNCA devuelve "text/markdown". Para un .md
//     devuelve "text/plain; charset=utf-8", y para un .md que empiece por HTML
//     devuelve "text/html; charset=utf-8". Comparar contra "text/markdown"
//     rechazaria con 415 el formato principal del proyecto.
//   - El valor devuelto lleva parametros (";charset=utf-8"), asi que hay que
//     normalizarlo con mime.ParseMediaType antes de compararlo con una lista.
func DetectFormat(head []byte, filename string) DetectResult {
	ext := strings.ToLower(filepath.Ext(filename))

	// 1. Firmas magicas: son la evidencia mas fuerte.
	if bytes.HasPrefix(head, magicPDF) {
		return DetectResult{Format: FormatPDF, MediaType: "application/pdf"}
	}
	if bytes.HasPrefix(head, magicZIP) {
		// Un ZIP puede ser DOCX, EPUB, ODT o un zip cualquiera. Solo la
		// extension permite distinguirlos sin abrir el contenedor; el parser
		// de DOCX confirmara despues buscando word/document.xml y devolvera
		// un fallo permanente si no lo encuentra.
		if ext == ".docx" {
			return DetectResult{
				Format:    FormatDOCX,
				MediaType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
				IsZIP:     true,
			}
		}
		return DetectResult{Format: FormatUnknown, MediaType: "application/zip", IsZIP: true}
	}

	sniffed := baseMediaType(http.DetectContentType(head))

	// 2. Extension conocida, coherente con un contenido textual.
	if f, ok := extToFormat[ext]; ok {
		switch f {
		case FormatMarkdown, FormatText, FormatHTML:
			if isTextual(sniffed) {
				return DetectResult{Format: f, MediaType: mediaTypeFor(f)}
			}
		}
	}

	// 3. Solo queda el sniffing.
	switch sniffed {
	case "text/html":
		return DetectResult{Format: FormatHTML, MediaType: "text/html"}
	case "text/plain":
		// Sin extension util, texto plano es la lectura conservadora: el
		// segmentador heuristico lo tratara y, si no hay estructura, producira
		// una sola unidad, que es un resultado valido (condicion C2).
		return DetectResult{Format: FormatText, MediaType: "text/plain"}
	}

	return DetectResult{Format: FormatUnknown, MediaType: sniffed}
}

func isTextual(mt string) bool {
	return strings.HasPrefix(mt, "text/") || mt == "application/octet-stream"
}

func mediaTypeFor(f Format) string {
	switch f {
	case FormatMarkdown:
		return "text/markdown"
	case FormatHTML:
		return "text/html"
	case FormatText:
		return "text/plain"
	case FormatDOCX:
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case FormatPDF:
		return "application/pdf"
	}
	return "application/octet-stream"
}

// baseMediaType descarta los parametros ("; charset=utf-8") de un media type.
func baseMediaType(v string) string {
	mt, _, err := mime.ParseMediaType(v)
	if err != nil {
		return v
	}
	return mt
}

// IsAllowedExtension comprueba la lista blanca de extensiones. Se aplica ANTES
// de leer el cuerpo completo, como primera barrera barata.
func IsAllowedExtension(filename string, pdfEnabled bool) bool {
	f, ok := extToFormat[strings.ToLower(filepath.Ext(filename))]
	if !ok {
		return false
	}
	if f == FormatPDF && !pdfEnabled {
		return false
	}
	return true
}

// AllowedExtensions devuelve la lista publicable en la UI y en el README.
func AllowedExtensions(pdfEnabled bool) []string {
	out := []string{".md", ".markdown", ".txt", ".html", ".htm", ".docx"}
	if pdfEnabled {
		out = append(out, ".pdf")
	}
	return out
}
