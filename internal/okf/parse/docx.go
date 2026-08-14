package parse

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf/docmodel"
)

// DOCXParser lee OOXML con la biblioteca estandar.
//
// Se descartan las librerias de terceros evaluadas: unidoc/unioffice es
// comercial, y nguyenthenguyen/docx o lukasjarosch/go-docx son motores de
// plantillas (buscar y reemplazar preservando runs) que no exponen estructura
// semantica. El subconjunto de OOXML que hace falta (p, pPr, pStyle, outlineLvl,
// r, rPr, t, numPr, drawing) es pequeno y estable desde 2007.
//
// La decodificacion es EN STREAMING por tokens y no un Unmarshal a una struct
// grande: encoding/xml con structs no preserva el orden relativo entre hijos de
// tipos distintos (un w:p y un w:tbl en el cuerpo), y el orden es exactamente lo
// que hay que conservar.
type DOCXParser struct{}

func (p *DOCXParser) Format() domain.Format { return domain.FormatDOCX }

func (p *DOCXParser) Parse(ctx context.Context, s *Source, o Options) (*docmodel.Document, error) {
	zr, err := zip.NewReader(s.R, s.Size)
	if err != nil {
		return nil, domain.PermanentFault("docx_not_zip", "El fichero .docx no es un contenedor OOXML valido", err)
	}
	if err := checkZipQuota(zr, o); err != nil {
		return nil, err
	}

	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		entries[f.Name] = f
	}

	docXML, ok := entries["word/document.xml"]
	if !ok {
		return nil, domain.PermanentFault("docx_no_document",
			"El fichero no contiene word/document.xml: no es un documento de Word", nil)
	}

	styles := parseStyles(entries["word/styles.xml"])
	rels := parseRels(entries["word/_rels/document.xml.rels"])

	doc := &docmodel.Document{SourceFormat: string(domain.FormatDOCX)}
	d := &docxConverter{
		doc: doc, styles: styles, rels: rels, entries: entries,
		assetsBudget: o.AssetsMax,
	}
	if err := d.convert(ctx, docXML); err != nil {
		return nil, err
	}

	if doc.Title == "" {
		doc.Title = firstHeadingText(doc)
	}
	return doc, ctx.Err()
}

// --- estilos ----------------------------------------------------------------

type styleInfo struct {
	level   int // nivel de encabezado derivado, 0 = no es encabezado
	basedOn string
}

// headingNamePattern reconoce el nombre canonico de los estilos de titulo en
// varios idiomas, YA NORMALIZADO (sin acentos, minusculas, sin separadores).
//
// Es imprescindible: Word en espanol genera styleId="Ttulo1" (la i acentuada se
// pierde al construir el identificador) y w:name "Titulo 1"; en aleman,
// "Uberschrift 1". Comparar contra "heading" a secas haria que un informe de 40
// paginas creado con Word en espanol cayera al camino sin encabezados y
// produjera una sola unidad, desperdiciando el formato de mayor fidelidad.
var headingNamePattern = regexp.MustCompile(`^(heading|titulo|ttulo|titre|uberschrift|berschrift|kop|rubrik|titolo)([1-9])$`)

func parseStyles(f *zip.File) map[string]styleInfo {
	out := map[string]styleInfo{}
	if f == nil {
		return out
	}
	rc, err := f.Open()
	if err != nil {
		return out
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	var (
		cur     string
		info    styleInfo
		inStyle bool
	)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			if ee, ok := tok.(xml.EndElement); ok && ee.Name.Local == "style" && inStyle {
				if info.level > 0 || info.basedOn != "" {
					out[cur] = info
				}
				inStyle = false
			}
			continue
		}
		switch se.Name.Local {
		case "style":
			inStyle = true
			cur = localAttr(se, "styleId")
			info = styleInfo{}
			if lvl := levelFromName(cur); lvl > 0 {
				info.level = lvl
			}
		case "name":
			if inStyle {
				if lvl := levelFromName(localAttr(se, "val")); lvl > 0 && info.level == 0 {
					info.level = lvl
				}
			}
		case "basedOn":
			if inStyle {
				info.basedOn = localAttr(se, "val")
			}
		case "outlineLvl":
			// Camino primario: es explicito y no depende del idioma.
			if inStyle {
				if v, err := strconv.Atoi(localAttr(se, "val")); err == nil && v >= 0 && v <= 8 {
					info.level = v + 1
				}
			}
		}
	}
	return out
}

func levelFromName(name string) int {
	m := headingNamePattern.FindStringSubmatch(normalizeStyleName(name))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[2])
	return n
}

// normalizeStyleName pasa "Título 1" -> "titulo1", "Überschrift 1" -> "uberschrift1".
func normalizeStyleName(s string) string {
	t := transform.Chain(norm.NFKD, runes.Remove(runes.In(unicode.Mn)), norm.NFC)
	out, _, err := transform.String(t, s)
	if err != nil {
		out = s
	}
	var b strings.Builder
	for _, r := range strings.ToLower(out) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (d *docxConverter) levelFor(styleID string) int {
	seen := map[string]bool{}
	for styleID != "" && !seen[styleID] {
		seen[styleID] = true
		info, ok := d.styles[styleID]
		if !ok {
			return levelFromName(styleID)
		}
		if info.level > 0 {
			return info.level
		}
		styleID = info.basedOn // herencia por w:basedOn
	}
	return 0
}

// --- relaciones -------------------------------------------------------------

type relInfo struct {
	target   string
	external bool
}

func parseRels(f *zip.File) map[string]relInfo {
	out := map[string]relInfo{}
	if f == nil {
		return out
	}
	rc, err := f.Open()
	if err != nil {
		return out
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			id := localAttr(se, "Id")
			out[id] = relInfo{
				target:   localAttr(se, "Target"),
				external: strings.EqualFold(localAttr(se, "TargetMode"), "External"),
			}
		}
	}
	return out
}

// --- conversion -------------------------------------------------------------

type docxConverter struct {
	doc     *docmodel.Document
	styles  map[string]styleInfo
	rels    map[string]relInfo
	entries map[string]*zip.File

	assetsBudget int64
}

func (d *docxConverter) convert(ctx context.Context, f *zip.File) error {
	rc, err := f.Open()
	if err != nil {
		return domain.PermanentFault("docx_open", "No se pudo leer word/document.xml", err)
	}
	defer rc.Close()

	dec := xml.NewDecoder(rc)
	depth := 0

	for {
		if depth%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return domain.PermanentFault("docx_xml", "El XML del documento esta malformado", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		depth++

		switch se.Name.Local {
		case "p":
			d.paragraph(dec, se)
		case "tbl":
			d.table(dec)
		}
	}
	return nil
}

// paragraph consume un <w:p> completo, conservando el orden de sus runs.
func (d *docxConverter) paragraph(dec *xml.Decoder, start xml.StartElement) {
	var (
		text     strings.Builder
		styleID  string
		outline  = -1
		numbered bool
		images   []*docmodel.Image
		bold     bool
	)

	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "pStyle":
				styleID = localAttr(t, "val")
			case "outlineLvl":
				if v, err := strconv.Atoi(localAttr(t, "val")); err == nil {
					outline = v
				}
			case "numPr":
				numbered = true
			case "b":
				bold = true
			case "t":
				var s string
				if err := dec.DecodeElement(&s, &t); err == nil {
					text.WriteString(s)
				}
			case "tab":
				text.WriteString(" ")
			case "br":
				text.WriteString("\n")
			case "blip":
				if id := localAttr(t, "embed"); id != "" {
					if img := d.image(id); img != nil {
						images = append(images, img)
					}
				}
			}
		case xml.EndElement:
			if t.Name.Local == start.Name.Local {
				d.emitParagraph(text.String(), styleID, outline, numbered, bold, images)
				return
			}
		}
	}
}

func (d *docxConverter) emitParagraph(text, styleID string, outline int, numbered, bold bool, images []*docmodel.Image) {
	for _, img := range images {
		d.doc.Blocks = append(d.doc.Blocks, docmodel.Block{Kind: docmodel.KindImage, Image: img})
	}

	txt := strings.TrimSpace(text)
	if txt == "" {
		return
	}

	// Prioridad de deteccion de nivel: outlineLvl del propio parrafo, luego el
	// estilo (que a su vez consulta outlineLvl, nombre canonico y herencia).
	level := 0
	if outline >= 0 && outline <= 8 {
		level = outline + 1
	} else if styleID != "" {
		level = d.levelFor(styleID)
	}

	switch {
	case level > 0:
		if level > 6 {
			level = 6
		}
		d.doc.Blocks = append(d.doc.Blocks, docmodel.Block{
			Kind: docmodel.KindHeading, Level: level, Text: txt,
		})
		if level == 1 && d.doc.Title == "" {
			d.doc.Title = txt
		}

	case numbered:
		// Agrupar items consecutivos en una sola lista.
		if n := len(d.doc.Blocks); n > 0 && d.doc.Blocks[n-1].Kind == docmodel.KindList {
			d.doc.Blocks[n-1].Items = append(d.doc.Blocks[n-1].Items, docmodel.ListItem{Text: txt})
			return
		}
		d.doc.Blocks = append(d.doc.Blocks, docmodel.Block{
			Kind:  docmodel.KindList,
			Items: []docmodel.ListItem{{Text: txt}},
		})

	default:
		if bold && len(txt) < 80 {
			// Parrafo corto enteramente en negrita sin estilo: se conserva como
			// parrafo con enfasis, no se asciende a encabezado. Ascenderlo
			// trocearia documentos por enfasis decorativo.
			txt = "**" + txt + "**"
		}
		d.doc.Blocks = append(d.doc.Blocks, docmodel.Block{Kind: docmodel.KindParagraph, Text: txt})
	}
}

func (d *docxConverter) table(dec *xml.Decoder) {
	blk := docmodel.Block{Kind: docmodel.KindTable}
	var row []string
	var cell strings.Builder

	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				var s string
				if err := dec.DecodeElement(&s, &t); err == nil {
					cell.WriteString(s)
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "tc":
				row = append(row, strings.TrimSpace(cell.String()))
				cell.Reset()
			case "tr":
				if len(row) > 0 {
					blk.Rows = append(blk.Rows, row)
				}
				row = nil
			case "tbl":
				if len(blk.Rows) > 0 {
					d.doc.Blocks = append(d.doc.Blocks, blk)
				}
				return
			}
		}
	}
}

// image extrae un recurso de word/media/ resolviendo la relacion.
func (d *docxConverter) image(relID string) *docmodel.Image {
	rel, ok := d.rels[relID]
	if !ok {
		return nil
	}
	if rel.external {
		// Recurso remoto: se conserva la referencia y NO se descarga.
		return &docmodel.Image{URL: rel.target, Remote: true}
	}

	name := path.Clean("word/" + rel.target)
	f, ok := d.entries[name]
	if !ok {
		return nil
	}
	if int64(f.UncompressedSize64) > d.assetsBudget {
		return nil
	}

	rc, err := f.Open()
	if err != nil {
		return nil
	}
	defer rc.Close()

	data, err := io.ReadAll(io.LimitReader(rc, d.assetsBudget))
	if err != nil || len(data) == 0 {
		return nil
	}
	d.assetsBudget -= int64(len(data))

	return &docmodel.Image{
		Alt:      strings.TrimSuffix(path.Base(name), path.Ext(name)),
		Data:     data,
		MimeType: mimeFromExt(path.Ext(name)),
	}
}

// --- utilidades -------------------------------------------------------------

func localAttr(se xml.StartElement, name string) string {
	for _, a := range se.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// checkZipQuota protege frente a bombas de descompresion. El documento de
// entrada es NO CONFIABLE: sin este limite, un .docx de 2 MB que descomprime a
// varios cientos deja al worker sin memoria y lo mata el OOM killer.
func checkZipQuota(zr *zip.Reader, o Options) error {
	if o.ZipMaxEntries > 0 && len(zr.File) > o.ZipMaxEntries {
		return domain.PermanentFault("zip_too_many_entries",
			fmt.Sprintf("el contenedor tiene mas de %d entradas", o.ZipMaxEntries), nil)
	}
	var compressed, uncompressed int64
	for _, f := range zr.File {
		compressed += int64(f.CompressedSize64)
		uncompressed += int64(f.UncompressedSize64)
	}
	if o.ZipMaxRatio > 0 && compressed > 0 &&
		uncompressed/compressed > int64(o.ZipMaxRatio) {
		return domain.PermanentFault("zip_bomb",
			"la relacion de descompresion del contenedor es sospechosa", nil)
	}
	if o.MaxBytes > 0 && uncompressed > o.MaxBytes*20 {
		return domain.PermanentFault("zip_too_large",
			"el contenido descomprimido supera el limite permitido", nil)
	}
	return nil
}

func mimeFromExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".svg":
		return "image/svg+xml"
	case ".bmp":
		return "image/bmp"
	}
	return "application/octet-stream"
}

func sprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }
