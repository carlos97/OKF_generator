package parse

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf/docmodel"
)

// MarkdownParser convierte Markdown a la IR usando el AST de goldmark.
//
// Se usa el AST y no expresiones regulares porque asi salen gratis los casos
// que de otro modo habria que tratar a mano y son fuente segura de bugs: los
// encabezados setext (subrayados con === o ---), las almohadillas dentro de
// bloques de codigo cercados, y el marcado anidado.
type MarkdownParser struct{}

func (p *MarkdownParser) Format() domain.Format { return domain.FormatMarkdown }

func (p *MarkdownParser) Parse(ctx context.Context, s *Source, o Options) (*docmodel.Document, error) {
	src, err := s.Bytes(o.MaxBytes)
	if err != nil {
		return nil, err
	}

	// El front-matter YAML se separa ANTES de tocar goldmark.
	//
	// Un bloque delimitado por --- no es Markdown: goldmark lo interpreta como
	// una linea horizontal, un parrafo y -por el --- de cierre- un encabezado
	// setext de nivel 2. El resultado seria un H2 fantasma cuyo texto son los
	// campos del front-matter, que contaminaria la segmentacion y se mostraria
	// al usuario como un titular gigante en la previsualizacion.
	_, body := splitFrontMatter(src)

	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	reader := text.NewReader(body)
	root := md.Parser().Parse(reader)

	doc := &docmodel.Document{SourceFormat: string(domain.FormatMarkdown)}
	conv := &mdConverter{src: body, opts: o, doc: doc}
	conv.walk(root)

	if doc.Title == "" {
		doc.Title = firstHeadingText(doc)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return doc, nil
}

type mdConverter struct {
	src  []byte
	opts Options
	doc  *docmodel.Document
}

func (c *mdConverter) walk(n ast.Node) {
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		c.block(child)
	}
}

func (c *mdConverter) block(n ast.Node) {
	switch v := n.(type) {
	case *ast.Heading:
		txt := c.inlineText(v)
		// Un encabezado vacio ("##" a secas, frecuente en plantillas) no es un
		// punto de corte util y goldmark no le asigna lineas: emitirlo como
		// encabezado produciria una unidad sin titulo.
		if strings.TrimSpace(txt) == "" {
			return
		}
		c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{
			Kind: docmodel.KindHeading, Level: v.Level, Text: txt,
		})
		if v.Level == 1 && c.doc.Title == "" {
			c.doc.Title = txt
		}

	case *ast.Paragraph:
		if img := c.soleImage(v); img != nil {
			c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{Kind: docmodel.KindImage, Image: img})
			return
		}
		txt := c.inlineMarkdown(v)
		if strings.TrimSpace(txt) != "" {
			c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{Kind: docmodel.KindParagraph, Text: txt})
		}

	case *ast.TextBlock:
		txt := c.inlineMarkdown(v)
		if strings.TrimSpace(txt) != "" {
			c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{Kind: docmodel.KindParagraph, Text: txt})
		}

	case *ast.FencedCodeBlock:
		var b bytes.Buffer
		lines := v.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			b.Write(seg.Value(c.src))
		}
		c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{
			Kind: docmodel.KindCode, Text: b.String(), Language: string(v.Language(c.src)),
		})

	case *ast.CodeBlock:
		var b bytes.Buffer
		lines := v.Lines()
		for i := 0; i < lines.Len(); i++ {
			seg := lines.At(i)
			b.Write(seg.Value(c.src))
		}
		c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{Kind: docmodel.KindCode, Text: b.String()})

	case *ast.List:
		blk := docmodel.Block{Kind: docmodel.KindList, Ordered: v.IsOrdered()}
		for li := v.FirstChild(); li != nil; li = li.NextSibling() {
			blk.Items = append(blk.Items, docmodel.ListItem{Text: c.inlineMarkdown(li)})
		}
		if len(blk.Items) > 0 {
			c.doc.Blocks = append(c.doc.Blocks, blk)
		}

	case *ast.Blockquote:
		c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{
			Kind: docmodel.KindQuote, Text: c.inlineMarkdown(v),
		})

	case *ast.ThematicBreak:
		c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{Kind: docmodel.KindRule})

	case *extast.Table:
		blk := docmodel.Block{Kind: docmodel.KindTable}
		for row := v.FirstChild(); row != nil; row = row.NextSibling() {
			var cells []string
			for cell := row.FirstChild(); cell != nil; cell = cell.NextSibling() {
				cells = append(cells, c.inlineMarkdown(cell))
			}
			if len(cells) > 0 {
				blk.Rows = append(blk.Rows, cells)
			}
		}
		if len(blk.Rows) > 0 {
			c.doc.Blocks = append(c.doc.Blocks, blk)
		}

	case *ast.HTMLBlock:
		// El HTML crudo dentro del Markdown se descarta: el bundle de salida
		// debe ser Markdown limpio, y conservarlo abriria una via de XSS en la
		// previsualizacion del frontend.
		c.doc.AddNote("OKF-SEC-001", "Se descarto un bloque de HTML embebido en el documento de origen")

	default:
		c.walk(n)
	}
}

// soleImage detecta un parrafo cuyo unico contenido es una imagen, para
// emitirlo como bloque de imagen y poder extraer el recurso.
func (c *mdConverter) soleImage(n ast.Node) *docmodel.Image {
	var img *ast.Image
	count := 0
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch v := child.(type) {
		case *ast.Image:
			img = v
			count++
		case *ast.Text:
			if strings.TrimSpace(string(v.Segment.Value(c.src))) != "" {
				return nil
			}
		default:
			return nil
		}
	}
	if count != 1 || img == nil {
		return nil
	}
	return c.imageFrom(img)
}

func (c *mdConverter) imageFrom(v *ast.Image) *docmodel.Image {
	dest := string(v.Destination)
	out := &docmodel.Image{
		Alt:   string(v.Text(c.src)),
		Title: string(v.Title),
		URL:   dest,
	}
	if data, mime, ok := decodeDataURI(dest); ok {
		out.Data = data
		out.MimeType = mime
	} else if strings.HasPrefix(dest, "http://") || strings.HasPrefix(dest, "https://") {
		out.Remote = true
	}
	return out
}

// inlineText devuelve el texto plano de un nodo (para titulos).
func (c *mdConverter) inlineText(n ast.Node) string {
	var b strings.Builder
	ast.Walk(n, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch v := node.(type) {
		case *ast.Text:
			b.Write(v.Segment.Value(c.src))
		case *ast.String:
			b.Write(v.Value)
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(b.String())
}

// inlineMarkdown reconstruye el Markdown en linea (enfasis, enlaces, codigo).
func (c *mdConverter) inlineMarkdown(n ast.Node) string {
	var b strings.Builder
	c.renderInline(&b, n)
	return strings.TrimSpace(b.String())
}

func (c *mdConverter) renderInline(b *strings.Builder, n ast.Node) {
	for child := n.FirstChild(); child != nil; child = child.NextSibling() {
		switch v := child.(type) {
		case *ast.Text:
			b.Write(v.Segment.Value(c.src))
			if v.SoftLineBreak() {
				b.WriteByte('\n')
			}
			if v.HardLineBreak() {
				b.WriteString("  \n")
			}
		case *ast.String:
			b.Write(v.Value)
		case *ast.Emphasis:
			mark := strings.Repeat("*", v.Level)
			b.WriteString(mark)
			c.renderInline(b, v)
			b.WriteString(mark)
		case *ast.CodeSpan:
			b.WriteByte('`')
			c.renderInline(b, v)
			b.WriteByte('`')
		case *ast.Link:
			b.WriteByte('[')
			c.renderInline(b, v)
			b.WriteString("](")
			b.WriteString(string(v.Destination))
			b.WriteByte(')')
		case *ast.AutoLink:
			b.WriteString(string(v.URL(c.src)))
		case *ast.Image:
			b.WriteString("![")
			b.WriteString(string(v.Text(c.src)))
			b.WriteString("](")
			b.WriteString(string(v.Destination))
			b.WriteByte(')')
		case *extast.Strikethrough:
			b.WriteString("~~")
			c.renderInline(b, v)
			b.WriteString("~~")
		case *ast.RawHTML, *ast.HTMLBlock:
			// omitido a proposito (ver HTMLBlock arriba)
		default:
			c.renderInline(b, child)
		}
	}
}

// splitFrontMatter separa un bloque YAML delimitado por --- al inicio.
func splitFrontMatter(src []byte) (fm, body []byte) {
	if !bytes.HasPrefix(src, []byte("---\n")) && !bytes.HasPrefix(src, []byte("---\r\n")) {
		return nil, src
	}
	rest := src[3:]
	if i := bytes.IndexByte(rest, '\n'); i >= 0 {
		rest = rest[i+1:]
	}
	end := bytes.Index(rest, []byte("\n---"))
	if end < 0 {
		return nil, src
	}
	fm = rest[:end]
	after := rest[end+4:]
	if i := bytes.IndexByte(after, '\n'); i >= 0 {
		after = after[i+1:]
	} else {
		after = nil
	}
	return fm, after
}

// decodeDataURI extrae los bytes de un recurso embebido como data URI.
func decodeDataURI(s string) ([]byte, string, bool) {
	if !strings.HasPrefix(s, "data:") {
		return nil, "", false
	}
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return nil, "", false
	}
	meta := s[5:comma]
	payload := s[comma+1:]
	if !strings.Contains(meta, ";base64") {
		return nil, "", false
	}
	mime := strings.TrimSuffix(meta, ";base64")
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", false
	}
	return data, mime, true
}

func firstHeadingText(d *docmodel.Document) string {
	for _, b := range d.Blocks {
		if b.Kind == docmodel.KindHeading {
			return b.Text
		}
	}
	return ""
}

var _ = fmt.Sprintf
