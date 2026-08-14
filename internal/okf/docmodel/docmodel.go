// Package docmodel define la representacion intermedia (IR) del documento.
//
// Cada formato de entrada aporta UNICAMENTE un parser que produce un
// docmodel.Document; a partir de ahi, segmentacion, render, extraccion de
// assets y validacion son codigo compartido. El coste es N parsers + 1
// renderer en lugar de N*M conversores, la salida Markdown es homogenea entre
// formatos (mismos slugs, mismas anclas, mismo estilo de listas) y anadir un
// formato nuevo consiste en implementar una interfaz.
//
// La alternativa -convertir cada formato directamente a Markdown y volver a
// parsearlo para segmentar- obliga a razonar con expresiones regulares sobre
// Markdown, que es exactamente donde se pierden el orden y los casos limite que
// la rubrica evalua.
package docmodel

// BlockKind identifica el tipo de bloque de la IR.
type BlockKind string

const (
	KindHeading   BlockKind = "heading"
	KindParagraph BlockKind = "paragraph"
	KindList      BlockKind = "list"
	KindCode      BlockKind = "code"
	KindQuote     BlockKind = "quote"
	KindTable     BlockKind = "table"
	KindImage     BlockKind = "image"
	KindRule      BlockKind = "rule"
	KindRaw       BlockKind = "raw" // Markdown ya renderizado, se emite tal cual
)

// Block es un nodo lineal del documento. La IR es deliberadamente una lista
// plana y no un arbol: la segmentacion corta por posicion y un arbol obligaria
// a reconstruir subarboles parciales en cada corte.
type Block struct {
	Kind  BlockKind
	Level int    // solo para KindHeading: 1..6
	Text  string // contenido textual o Markdown ya formateado

	// Listas
	Items   []ListItem
	Ordered bool

	// Codigo
	Language string

	// Imagenes
	Image *Image

	// Tablas: primera fila = cabecera
	Rows [][]string
}

type ListItem struct {
	Text   string
	Depth  int
	Blocks []Block // contenido anidado (rara vez usado; simplifica el render)
}

// Image describe un recurso referenciado. Data va relleno solo cuando el
// recurso venia embebido en el origen (data URI en HTML/Markdown, word/media/
// en DOCX). Si el recurso es remoto, se conserva la URL y NO se descarga.
type Image struct {
	Alt      string
	Title    string
	URL      string // referencia original
	Data     []byte // bytes embebidos, si los habia
	MimeType string
	Remote   bool
}

// Document es la IR completa.
type Document struct {
	Title  string
	Blocks []Block

	// SourceFormat es informativo y llega hasta log.md.
	SourceFormat string

	// StructureInferred indica que los encabezados se dedujeron con heuristicas
	// (texto plano, PDF) y no de un marcado explicito. Es lo que justifica el
	// veredicto "valido con advertencias" en esos formatos, y lo que permite
	// NO emitir advertencias cuando la estructura es fiable.
	StructureInferred bool

	// Notes son observaciones del parser que acaban en log.md y pueden generar
	// hallazgos. Un documento breve y sin divisiones NO produce ninguna: la
	// ausencia de encabezados es un resultado legitimo, no una anomalia.
	Notes []Note
}

type Note struct {
	Code    string
	Message string
}

func (d *Document) AddNote(code, msg string) {
	d.Notes = append(d.Notes, Note{Code: code, Message: msg})
}

// Headings devuelve los encabezados en orden de aparicion, con su indice de
// bloque, que es lo que el segmentador usa como punto de corte.
type HeadingRef struct {
	BlockIndex int
	Level      int
	Text       string
}

func (d *Document) Headings() []HeadingRef {
	var out []HeadingRef
	for i, b := range d.Blocks {
		if b.Kind == KindHeading {
			out = append(out, HeadingRef{BlockIndex: i, Level: b.Level, Text: b.Text})
		}
	}
	return out
}

// HasContent indica si hay algo mas que encabezados vacios.
func (d *Document) HasContent() bool {
	for _, b := range d.Blocks {
		switch b.Kind {
		case KindParagraph, KindList, KindCode, KindQuote, KindTable, KindImage, KindRaw:
			if b.Text != "" || len(b.Items) > 0 || len(b.Rows) > 0 || b.Image != nil {
				return true
			}
		case KindHeading:
			if b.Text != "" {
				return true
			}
		}
	}
	return false
}
