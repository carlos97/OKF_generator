package parse

import (
	"bytes"
	"context"
	"encoding/xml"
	"io"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf/docmodel"
)

// PDFParser extrae estructura de un PDF invocando pdftohtml de poppler-utils.
//
// El problema de fondo es que un PDF no tiene estructura semantica: no existe
// "esto es un encabezado", solo texto colocado en una posicion con un tamano de
// fuente. Las librerias Go puras evaluadas (ledongthuc/pdf, dslipak/pdf) devuelven
// texto plano sin tamanos, con lo que la unica heuristica posible seria "linea
// corta en mayusculas", que es inservible. `pdftohtml -xml` emite <fontspec> con
// el tamano de cada fuente, y eso es lo unico que hace viable inferir jerarquia.
//
// Aun asi, la deteccion es una INFERENCIA y se marca como tal: el documento sale
// con StructureInferred, lo que acaba produciendo un veredicto "valido con
// advertencias". Es la lectura honesta y encaja con el eje de clasificacion que
// el enunciado pide distinguir.
//
// El soporte queda desactivado si la imagen del worker no trae poppler: el
// error es explicito y permanente, no un fallo silencioso.
type PDFParser struct{}

func (p *PDFParser) Format() domain.Format { return domain.FormatPDF }

// Available indica si poppler esta instalado en la imagen.
func PDFAvailable() bool {
	_, err := exec.LookPath("pdftohtml")
	return err == nil
}

func (p *PDFParser) Parse(ctx context.Context, s *Source, o Options) (*docmodel.Document, error) {
	if !PDFAvailable() {
		return nil, domain.PermanentFault("pdf_unavailable",
			"El soporte de PDF requiere poppler-utils en la imagen del worker", nil)
	}

	raw, err := s.Bytes(o.MaxBytes)
	if err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp("", "okf-*.pdf")
	if err != nil {
		return nil, domain.TransientFault("pdf_tmp", "No se pudo crear el fichero temporal", err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return nil, domain.TransientFault("pdf_tmp", "No se pudo escribir el fichero temporal", err)
	}
	tmp.Close()

	// -i ignora imagenes, -stdout escribe el XML a la salida estandar,
	// -hidden incluye texto no visible para no perder contenido.
	cmd := exec.CommandContext(ctx, "pdftohtml", "-xml", "-i", "-hidden", "-stdout", tmp.Name())
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return nil, domain.PermanentFault("pdf_extract",
			"No se pudo extraer el texto del PDF: "+strings.TrimSpace(errBuf.String()), err)
	}

	doc, err := parsePDFXML(out.Bytes())
	if err != nil {
		return nil, err
	}
	doc.SourceFormat = string(domain.FormatPDF)
	doc.StructureInferred = true
	doc.AddNote("OKF-STR-PDF",
		"La estructura del PDF se dedujo a partir del tamano de fuente; puede no reflejar la jerarquia original")
	return doc, ctx.Err()
}

type pdfText struct {
	Top  int    `xml:"top,attr"`
	Left int    `xml:"left,attr"`
	Font int    `xml:"font,attr"`
	Text string `xml:",chardata"`
}

type pdfFontSpec struct {
	ID   int `xml:"id,attr"`
	Size int `xml:"size,attr"`
}

func parsePDFXML(data []byte) (*docmodel.Document, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false

	fonts := map[int]int{} // id -> tamano
	var lines []pdfText

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, domain.PermanentFault("pdf_xml", "La salida de pdftohtml no es XML valido", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "fontspec":
			var fs pdfFontSpec
			if err := dec.DecodeElement(&fs, &se); err == nil {
				fonts[fs.ID] = fs.Size
			}
		case "text":
			var t pdfText
			if err := dec.DecodeElement(&t, &se); err == nil {
				t.Text = strings.TrimSpace(stripTags(t.Text))
				if t.Text != "" {
					lines = append(lines, t)
				}
			}
		}
	}

	// El tamano de fuente predominante es el del cuerpo; todo lo notablemente
	// mayor es candidato a encabezado.
	body := dominantSize(lines, fonts)

	doc := &docmodel.Document{}
	var para []string
	flush := func() {
		if len(para) == 0 {
			return
		}
		doc.Blocks = append(doc.Blocks, docmodel.Block{
			Kind: docmodel.KindParagraph, Text: strings.Join(para, " "),
		})
		para = nil
	}

	for _, l := range lines {
		size := fonts[l.Font]
		switch {
		case body > 0 && size >= body*3/2:
			flush()
			doc.Blocks = append(doc.Blocks, docmodel.Block{Kind: docmodel.KindHeading, Level: 1, Text: l.Text})
			if doc.Title == "" {
				doc.Title = l.Text
			}
		case body > 0 && size >= body*6/5:
			flush()
			doc.Blocks = append(doc.Blocks, docmodel.Block{Kind: docmodel.KindHeading, Level: 2, Text: l.Text})
		default:
			para = append(para, l.Text)
		}
	}
	flush()

	if doc.Title == "" {
		doc.Title = firstHeadingText(doc)
	}
	return doc, nil
}

func dominantSize(lines []pdfText, fonts map[int]int) int {
	count := map[int]int{}
	for _, l := range lines {
		count[fonts[l.Font]] += len(l.Text)
	}
	type kv struct{ size, weight int }
	var all []kv
	for s, w := range count {
		if s > 0 {
			all = append(all, kv{s, w})
		}
	}
	if len(all) == 0 {
		return 0
	}
	sort.Slice(all, func(i, j int) bool { return all[i].weight > all[j].weight })
	return all[0].size
}

// stripTags elimina el marcado inline (<b>, <i>) que pdftohtml deja dentro de
// los nodos de texto.
func stripTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

var _ = strconv.Itoa
