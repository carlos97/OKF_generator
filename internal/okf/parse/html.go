package parse

import (
	"context"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf/docmodel"
)

// HTMLParser convierte HTML a la IR.
//
// ORDEN CRITICO: los recursos embebidos como data URI se extraen ANTES de
// sanear. La politica UGCPolicy de bluemonday no permite <img> en absoluto y
// ninguna politica admite el esquema data:, de modo que sanear primero
// eliminaria justo los atributos de los que depende la extraccion de assets, y
// las imagenes se perderian sin que nada lo advirtiera.
//
// Aqui no se usa bluemonday: al convertir a la IR nos quedamos unicamente con
// los nodos que sabemos interpretar y descartamos el resto (script, style,
// nav, header, footer, aside, iframe...). Es una lista blanca por
// construccion, mas estricta que cualquier saneador, y ademas produce Markdown
// limpio en lugar de HTML "casi seguro".
type HTMLParser struct{}

func (p *HTMLParser) Format() domain.Format { return domain.FormatHTML }

var skipTags = map[atom.Atom]bool{
	atom.Script: true, atom.Style: true, atom.Noscript: true,
	atom.Nav: true, atom.Header: true, atom.Footer: true, atom.Aside: true,
	atom.Iframe: true, atom.Object: true, atom.Embed: true, atom.Svg: true,
	atom.Form: true, atom.Button: true, atom.Input: true, atom.Select: true,
	atom.Template: true, atom.Canvas: true, atom.Audio: true, atom.Video: true,
}

func (p *HTMLParser) Parse(ctx context.Context, s *Source, o Options) (*docmodel.Document, error) {
	raw, err := s.Bytes(o.MaxBytes)
	if err != nil {
		return nil, err
	}

	root, err := html.Parse(strings.NewReader(string(raw)))
	if err != nil {
		return nil, domain.PermanentFault("html_parse", "El documento HTML no se pudo interpretar", err)
	}

	doc := &docmodel.Document{SourceFormat: string(domain.FormatHTML)}
	c := &htmlConverter{doc: doc}

	// Preferir <main> o <article> si existen: evita arrastrar menus y pies.
	if main := findFirst(root, atom.Main, atom.Article); main != nil {
		c.walk(main)
	} else if body := findFirst(root, atom.Body); body != nil {
		c.walk(body)
	} else {
		c.walk(root)
	}
	c.flush()

	if doc.Title == "" {
		if t := findFirst(root, atom.Title); t != nil {
			doc.Title = strings.TrimSpace(textOf(t))
		}
	}
	if doc.Title == "" {
		doc.Title = firstHeadingText(doc)
	}
	if c.dropped > 0 {
		doc.AddNote("OKF-CVG-002",
			plural(c.dropped, "Se descarto %d bloque de navegacion, script o pie de pagina del HTML de origen",
				"Se descartaron %d bloques de navegacion, script o pie de pagina del HTML de origen"))
	}
	return doc, ctx.Err()
}

type htmlConverter struct {
	doc     *docmodel.Document
	inline  strings.Builder
	dropped int
}

func (c *htmlConverter) flush() {
	txt := strings.TrimSpace(collapseSpaces(c.inline.String()))
	c.inline.Reset()
	if txt != "" {
		c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{Kind: docmodel.KindParagraph, Text: txt})
	}
}

func (c *htmlConverter) walk(n *html.Node) {
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		c.node(child)
	}
}

func (c *htmlConverter) node(n *html.Node) {
	switch n.Type {
	case html.TextNode:
		c.inline.WriteString(n.Data)
		return
	case html.ElementNode:
	default:
		return
	}

	if skipTags[n.DataAtom] {
		c.dropped++
		return
	}

	switch n.DataAtom {
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		c.flush()
		level := int(n.Data[1] - '0')
		txt := strings.TrimSpace(collapseSpaces(textOf(n)))
		if txt != "" {
			c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{
				Kind: docmodel.KindHeading, Level: level, Text: txt,
			})
			if level == 1 && c.doc.Title == "" {
				c.doc.Title = txt
			}
		}

	case atom.P, atom.Div, atom.Section, atom.Figcaption:
		c.flush()
		c.walk(n)
		c.flush()

	case atom.Br:
		c.inline.WriteString("\n")

	case atom.Strong, atom.B:
		c.inline.WriteString("**")
		c.walk(n)
		c.inline.WriteString("**")

	case atom.Em, atom.I:
		c.inline.WriteString("*")
		c.walk(n)
		c.inline.WriteString("*")

	case atom.Code:
		c.inline.WriteString("`")
		c.walk(n)
		c.inline.WriteString("`")

	case atom.A:
		href := attr(n, "href")
		// javascript: y data: en enlaces no sobreviven la conversion.
		if isUnsafeURL(href) {
			c.walk(n)
			return
		}
		c.inline.WriteString("[")
		c.walk(n)
		c.inline.WriteString("](" + href + ")")

	case atom.Img:
		c.flush()
		src := attr(n, "src")
		img := &docmodel.Image{Alt: attr(n, "alt"), Title: attr(n, "title"), URL: src}
		if data, mime, ok := decodeDataURI(src); ok {
			img.Data = data
			img.MimeType = mime
		} else if strings.HasPrefix(src, "http://") || strings.HasPrefix(src, "https://") {
			img.Remote = true
		} else if isUnsafeURL(src) {
			return
		}
		c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{Kind: docmodel.KindImage, Image: img})

	case atom.Pre:
		c.flush()
		c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{
			Kind: docmodel.KindCode, Text: textOf(n),
		})

	case atom.Ul, atom.Ol:
		c.flush()
		blk := docmodel.Block{Kind: docmodel.KindList, Ordered: n.DataAtom == atom.Ol}
		for li := n.FirstChild; li != nil; li = li.NextSibling {
			if li.Type == html.ElementNode && li.DataAtom == atom.Li {
				sub := &htmlConverter{doc: &docmodel.Document{}}
				sub.walk(li)
				txt := strings.TrimSpace(collapseSpaces(sub.inline.String()))
				if txt != "" {
					blk.Items = append(blk.Items, docmodel.ListItem{Text: txt})
				}
			}
		}
		if len(blk.Items) > 0 {
			c.doc.Blocks = append(c.doc.Blocks, blk)
		}

	case atom.Blockquote:
		c.flush()
		c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{
			Kind: docmodel.KindQuote, Text: strings.TrimSpace(collapseSpaces(textOf(n))),
		})

	case atom.Table:
		c.flush()
		blk := docmodel.Block{Kind: docmodel.KindTable}
		var walkRows func(*html.Node)
		walkRows = func(x *html.Node) {
			for ch := x.FirstChild; ch != nil; ch = ch.NextSibling {
				if ch.Type != html.ElementNode {
					continue
				}
				if ch.DataAtom == atom.Tr {
					var cells []string
					for cell := ch.FirstChild; cell != nil; cell = cell.NextSibling {
						if cell.Type == html.ElementNode &&
							(cell.DataAtom == atom.Td || cell.DataAtom == atom.Th) {
							cells = append(cells, strings.TrimSpace(collapseSpaces(textOf(cell))))
						}
					}
					if len(cells) > 0 {
						blk.Rows = append(blk.Rows, cells)
					}
				} else {
					walkRows(ch)
				}
			}
		}
		walkRows(n)
		if len(blk.Rows) > 0 {
			c.doc.Blocks = append(c.doc.Blocks, blk)
		}

	case atom.Hr:
		c.flush()
		c.doc.Blocks = append(c.doc.Blocks, docmodel.Block{Kind: docmodel.KindRule})

	default:
		c.walk(n)
	}
}

// --- utilidades -------------------------------------------------------------

func findFirst(n *html.Node, atoms ...atom.Atom) *html.Node {
	want := map[atom.Atom]bool{}
	for _, a := range atoms {
		want[a] = true
	}
	var found *html.Node
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if found != nil {
			return
		}
		if x.Type == html.ElementNode && want[x.DataAtom] {
			found = x
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return found
}

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, name) {
			return a.Val
		}
	}
	return ""
}

func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			b.WriteString(x.Data)
			return
		}
		if x.Type == html.ElementNode && skipTags[x.DataAtom] {
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

func collapseSpaces(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch r {
		case ' ', '\t', '\r':
			space = true
		case '\n':
			b.WriteRune('\n')
			space = false
		default:
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = false
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isUnsafeURL(u string) bool {
	l := strings.ToLower(strings.TrimSpace(u))
	return strings.HasPrefix(l, "javascript:") ||
		strings.HasPrefix(l, "vbscript:") ||
		(strings.HasPrefix(l, "data:") && !strings.HasPrefix(l, "data:image/"))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return sprintf(one, n)
	}
	return sprintf(many, n)
}
