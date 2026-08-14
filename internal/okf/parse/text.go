package parse

import (
	"context"
	"strings"
	"unicode"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf/docmodel"
)

// TextParser trata texto plano con encabezados detectados por heuristica.
//
// A diferencia de Markdown o DOCX, aqui la estructura se DEDUCE, asi que el
// documento se marca con StructureInferred: es la senal honesta que despues
// justifica un veredicto "valido con advertencias" en lugar de fingir la misma
// confianza que un formato con marcado explicito.
type TextParser struct{}

func (p *TextParser) Format() domain.Format { return domain.FormatText }

func (p *TextParser) Parse(ctx context.Context, s *Source, o Options) (*docmodel.Document, error) {
	raw, err := s.Bytes(o.MaxBytes)
	if err != nil {
		return nil, err
	}
	// Normalizacion de fin de linea: el mismo documento subido desde Windows y
	// desde Linux debe producir el mismo bundle.
	content := strings.ReplaceAll(string(raw), "\r\n", "\n")
	lines := strings.Split(content, "\n")

	doc := &docmodel.Document{SourceFormat: string(domain.FormatText)}
	var para []string
	detected := 0

	flush := func() {
		if len(para) == 0 {
			return
		}
		txt := strings.TrimSpace(strings.Join(para, "\n"))
		if txt != "" {
			doc.Blocks = append(doc.Blocks, docmodel.Block{Kind: docmodel.KindParagraph, Text: txt})
		}
		para = nil
	}

	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		trimmed := strings.TrimSpace(line)

		if trimmed == "" {
			flush()
			continue
		}

		// Subrayado tipo setext: la linea siguiente es toda === o ---.
		if i+1 < len(lines) {
			if lvl := underlineLevel(strings.TrimSpace(lines[i+1])); lvl > 0 && len(trimmed) > 0 {
				flush()
				doc.Blocks = append(doc.Blocks, docmodel.Block{
					Kind: docmodel.KindHeading, Level: lvl, Text: trimmed,
				})
				if lvl == 1 && doc.Title == "" {
					doc.Title = trimmed
				}
				detected++
				i++ // consumir el subrayado
				continue
			}
		}

		if lvl, title, ok := headingHeuristic(trimmed); ok {
			flush()
			doc.Blocks = append(doc.Blocks, docmodel.Block{
				Kind: docmodel.KindHeading, Level: lvl, Text: title,
			})
			if lvl == 1 && doc.Title == "" {
				doc.Title = title
			}
			detected++
			continue
		}

		para = append(para, line)
	}
	flush()

	if detected > 0 {
		doc.StructureInferred = true
	}
	if doc.Title == "" {
		doc.Title = firstHeadingText(doc)
	}
	return doc, ctx.Err()
}

func underlineLevel(s string) int {
	if len(s) < 3 {
		return 0
	}
	switch {
	case strings.Trim(s, "=") == "":
		return 1
	case strings.Trim(s, "-") == "":
		return 2
	}
	return 0
}

// headingHeuristic reconoce dos patrones frecuentes en texto plano:
// numeracion jerarquica ("1.", "2.3") y lineas cortas en mayusculas.
//
// El umbral de longitud es deliberadamente conservador: preferimos no detectar
// un encabezado a trocear el documento por una frase suelta en mayusculas.
func headingHeuristic(line string) (int, string, bool) {
	if len(line) > 90 {
		return 0, "", false
	}

	// "1. Titulo" / "2.3 Titulo" / "III. Titulo"
	if idx := strings.IndexFunc(line, func(r rune) bool { return r == ' ' || r == '\t' }); idx > 0 {
		prefix := line[:idx]
		rest := strings.TrimSpace(line[idx:])
		if rest != "" && isNumberedPrefix(prefix) {
			level := strings.Count(strings.TrimSuffix(prefix, "."), ".") + 1
			if level > 3 {
				level = 3
			}
			return level, rest, true
		}
	}

	// Linea corta enteramente en mayusculas, con al menos una letra.
	hasLetter := false
	for _, r := range line {
		if unicode.IsLetter(r) {
			hasLetter = true
			if unicode.IsLower(r) {
				return 0, "", false
			}
		}
	}
	if hasLetter && len(line) <= 60 && !strings.HasSuffix(line, ".") {
		return 2, strings.TrimSpace(line), true
	}

	return 0, "", false
}

func isNumberedPrefix(p string) bool {
	p = strings.TrimSuffix(p, ".")
	if p == "" {
		return false
	}
	for _, r := range p {
		if !unicode.IsDigit(r) && r != '.' {
			return false
		}
	}
	return true
}
