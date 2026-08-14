// Package render escribe las unidades y los ficheros de navegacion y
// trazabilidad del bundle.
package render

import (
	"fmt"
	"strings"

	"github.com/uniandes-isis4426/okfp/internal/okf/docmodel"
	"github.com/uniandes-isis4426/okfp/internal/okf/segment"
)

// Version del formato de bundle que emitimos.
const OKFVersion = "0.1"

// Centinelas del bloque de indice.
//
// Sin ellos, la regla "los enlaces del indice aparecen en el mismo orden que
// los conceptos" no tendria forma de distinguir un enlace de concepto del
// enlace a log.md o de un enlace que venga de la prosa del documento original,
// y acabaria invalidando bundles perfectamente correctos. Delimitar el bloque
// convierte una regla ambigua en una comprobacion exacta.
const (
	TOCOpen  = "<!-- okf:toc -->"
	TOCClose = "<!-- /okf:toc -->"
	NavOpen  = "<!-- okf:nav -->"
	NavClose = "<!-- /okf:nav -->"
)

// Meta son los datos del bundle que van al front-matter.
type Meta struct {
	BundleID   string
	JobID      string
	SourceName string
	SourceFmt  string
	Title      string
	UnitCount  int
	// GeneratedAt se deriva de la fecha de creacion del trabajo, NO de
	// time.Now(): asi reprocesar el mismo trabajo produce el mismo contenido y
	// la afirmacion de determinismo es verificable.
	GeneratedAt string
}

// Concept renderiza un documento de concepto.
func Concept(u segment.Unit, meta Meta, prev, next *segment.Unit, anchors map[string]string) []byte {
	var b strings.Builder

	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("okf_version: %q\n", OKFVersion))
	b.WriteString("type: \"concept\"\n")
	b.WriteString(fmt.Sprintf("bundle_id: %q\n", meta.BundleID))
	b.WriteString(fmt.Sprintf("title: %q\n", u.Title))
	b.WriteString(fmt.Sprintf("slug: %q\n", u.Slug))
	b.WriteString(fmt.Sprintf("order: %d\n", u.Order))
	b.WriteString("---\n\n")

	// El titulo se emite siempre como H1 del concepto. Si la unidad nacio de un
	// encabezado, ese encabezado se omite del cuerpo para no duplicarlo.
	b.WriteString("# " + escapeInline(u.Title) + "\n\n")

	blocks := u.Blocks
	if len(blocks) > 0 && blocks[0].Kind == docmodel.KindHeading && blocks[0].Text == u.Title {
		blocks = blocks[1:]
	}

	baseLevel := 0
	for _, blk := range blocks {
		if blk.Kind == docmodel.KindHeading {
			if baseLevel == 0 || blk.Level < baseLevel {
				baseLevel = blk.Level
			}
		}
	}

	writeBlocks(&b, blocks, baseLevel, anchors, u.Filename)

	b.WriteString("\n" + NavOpen + "\n")
	var nav []string
	if prev != nil {
		nav = append(nav, fmt.Sprintf("[<- %s](./%s)", escapeInline(prev.Title), prev.Filename))
	}
	nav = append(nav, "[Indice](./index.md)")
	if next != nil {
		nav = append(nav, fmt.Sprintf("[%s ->](./%s)", escapeInline(next.Title), next.Filename))
	}
	b.WriteString(strings.Join(nav, " | ") + "\n")
	b.WriteString(NavClose + "\n")

	return []byte(b.String())
}

// writeBlocks emite la IR como Markdown, renivelando los encabezados internos
// para que queden por debajo del H1 del concepto.
func writeBlocks(b *strings.Builder, blocks []docmodel.Block, baseLevel int, anchors map[string]string, selfFile string) {
	for _, blk := range blocks {
		switch blk.Kind {
		case docmodel.KindHeading:
			lvl := 2
			if baseLevel > 0 {
				lvl = blk.Level - baseLevel + 2
			}
			if lvl < 2 {
				lvl = 2
			}
			if lvl > 6 {
				lvl = 6
			}
			b.WriteString(strings.Repeat("#", lvl) + " " + escapeInline(blk.Text) + "\n\n")

		case docmodel.KindParagraph:
			b.WriteString(rewriteAnchors(blk.Text, anchors, selfFile) + "\n\n")

		case docmodel.KindList:
			for i, it := range blk.Items {
				marker := "- "
				if blk.Ordered {
					marker = fmt.Sprintf("%d. ", i+1)
				}
				b.WriteString(marker + rewriteAnchors(it.Text, anchors, selfFile) + "\n")
			}
			b.WriteString("\n")

		case docmodel.KindCode:
			lang := blk.Language
			b.WriteString("```" + lang + "\n")
			body := blk.Text
			if !strings.HasSuffix(body, "\n") {
				body += "\n"
			}
			b.WriteString(body)
			b.WriteString("```\n\n")

		case docmodel.KindQuote:
			for _, line := range strings.Split(blk.Text, "\n") {
				b.WriteString("> " + line + "\n")
			}
			b.WriteString("\n")

		case docmodel.KindTable:
			writeTable(b, blk.Rows)

		case docmodel.KindImage:
			if blk.Image == nil {
				continue
			}
			b.WriteString(fmt.Sprintf("![%s](%s)\n\n",
				escapeInline(blk.Image.Alt), blk.Image.URL))

		case docmodel.KindRule:
			b.WriteString("---\n\n")

		case docmodel.KindRaw:
			b.WriteString(blk.Text + "\n\n")
		}
	}
}

func writeTable(b *strings.Builder, rows [][]string) {
	if len(rows) == 0 {
		return
	}
	cols := 0
	for _, r := range rows {
		if len(r) > cols {
			cols = len(r)
		}
	}
	write := func(cells []string) {
		b.WriteString("|")
		for i := 0; i < cols; i++ {
			v := ""
			if i < len(cells) {
				v = strings.ReplaceAll(cells[i], "|", "\\|")
			}
			b.WriteString(" " + v + " |")
		}
		b.WriteString("\n")
	}
	write(rows[0])
	b.WriteString("|" + strings.Repeat(" --- |", cols) + "\n")
	for _, r := range rows[1:] {
		write(r)
	}
	b.WriteString("\n")
}

// rewriteAnchors reescribe los enlaces internos por ancla.
//
// Al partir el documento, un enlace como [ver](#instalacion) deja de resolver
// porque su destino quedo en OTRO fichero: el clic no hace nada y ninguna regla
// lo detectaria. Con el mapa de anclas construido durante la segmentacion, el
// destino pasa a ser ./NN-instalacion.md#instalacion y la navegacion del bundle
// funciona de verdad.
func rewriteAnchors(text string, anchors map[string]string, selfFile string) string {
	if len(anchors) == 0 || !strings.Contains(text, "](#") {
		return text
	}
	var out strings.Builder
	rest := text
	for {
		i := strings.Index(rest, "](#")
		if i < 0 {
			out.WriteString(rest)
			break
		}
		out.WriteString(rest[:i+2])
		rest = rest[i+2:] // rest empieza en "#..."
		end := strings.IndexByte(rest, ')')
		if end < 0 {
			out.WriteString(rest)
			break
		}
		anchor := rest[1:end]
		if file, ok := anchors[anchor]; ok && file != selfFile {
			out.WriteString("./" + file + "#" + anchor)
		} else {
			out.WriteString("#" + anchor)
		}
		out.WriteString(")")
		rest = rest[end+1:]
	}
	return out.String()
}

// escapeInline neutraliza el marcado que podria romper la estructura del
// Markdown generado cuando el texto procede del documento del usuario.
func escapeInline(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "[", "\\[")
	s = strings.ReplaceAll(s, "]", "\\]")
	return strings.TrimSpace(s)
}
