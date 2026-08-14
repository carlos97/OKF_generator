// Package slug genera nombres de fichero y anclas seguros y deterministas.
package slug

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"
)

const maxLen = 60

// Make convierte un titulo en un slug ASCII, en minusculas y separado por
// guiones.
//
// El nombre NUNCA procede directamente de datos del usuario sin pasar por aqui:
// un titulo puede contener barras, puntos suspensivos o "..", y escribir eso
// como nombre de fichero es una via directa a path traversal dentro del bundle.
// Al quedarnos solo con [a-z0-9-] el problema desaparece de raiz.
func Make(s string) string {
	// NFKD + eliminacion de marcas diacriticas: "Instalación" -> "instalacion".
	t := transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	normalized, _, err := transform.String(t, s)
	if err != nil {
		normalized = s
	}

	var b strings.Builder
	lastDash := true // evita guion inicial
	for _, r := range strings.ToLower(normalized) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}

	out := strings.Trim(b.String(), "-")
	if len(out) > maxLen {
		out = strings.Trim(out[:maxLen], "-")
	}
	if out == "" {
		out = "seccion"
	}
	return out
}

// Deduper garantiza unicidad anadiendo -2, -3... a las colisiones.
//
// Sin esto, dos secciones tituladas "Introduccion" producirian el mismo nombre
// de fichero: el segundo concepto sobreescribiria al primero y el indice
// quedaria con un enlace que apunta al contenido equivocado.
type Deduper struct {
	seen map[string]int
}

func NewDeduper() *Deduper { return &Deduper{seen: make(map[string]int)} }

func (d *Deduper) Unique(s string) string {
	n := d.seen[s]
	d.seen[s] = n + 1
	if n == 0 {
		return s
	}
	return fmt.Sprintf("%s-%d", s, n+1)
}
