// Package segment parte el documento en unidades logicas.
//
// Esta es la pieza que decide dos de las condiciones verificables del
// enunciado, asi que la regla esta especificada de forma cerrada y cubierta por
// tests de tabla en vez de improvisarse durante la implementacion.
package segment

import (
	"fmt"

	"github.com/uniandes-isis4426/okfp/internal/okf/docmodel"
	"github.com/uniandes-isis4426/okfp/internal/okf/slug"
)

// SingleUnitName es el nombre del unico concepto cuando el documento no tiene
// divisiones. El enunciado lo pide explicitamente en su estructura minima.
const SingleUnitName = "documento"

// Unit es una unidad logica ya resuelta: su titulo, su posicion y el nombre de
// fichero con el que se publicara.
type Unit struct {
	Order    int // 1..N, el orden del documento de origen
	Title    string
	Slug     string
	Filename string
	Anchor   string // ancla del encabezado que la abre
	Blocks   []docmodel.Block

	// Synthetic indica que la unidad no nacio de un encabezado, sino que
	// recoge el contenido anterior al primer corte (preambulo) o el documento
	// entero cuando no hay encabezados.
	Synthetic bool
}

// Result agrupa las unidades y el mapa de anclas.
type Result struct {
	Units []Unit

	// SplitLevel es el nivel de encabezado usado como corte. 0 significa
	// "ninguno": el documento produjo una sola unidad.
	SplitLevel int

	// AnchorToFile mapea el ancla de CUALQUIER encabezado (no solo los de
	// corte) al fichero donde acabo. Sirve para reescribir los enlaces internos
	// del documento original, que al partir dejarian de resolver porque su
	// destino queda en otro fichero.
	AnchorToFile map[string]string
}

// Split aplica la regla de segmentacion.
//
// REGLA (cerrada, y la misma que verifican los tests):
//
//  1. Se consideran los encabezados de nivel <= 3.
//  2. El NIVEL DE CORTE es el menor nivel que aparece dos o mas veces. Si
//     ningun nivel se repite, es el menor nivel presente. Si no hay
//     encabezados, no hay corte.
//  3. Todo el contenido ANTERIOR al primer encabezado del nivel de corte forma
//     una unidad de preambulo. Nunca se descarta contenido: si se partiera solo
//     por los encabezados de corte, la introduccion que va bajo el "# Titulo"
//     de un documento tipico desapareceria del bundle en silencio.
//  4. Los encabezados mas profundos que el nivel de corte quedan DENTRO de su
//     concepto; no lo parten.
//  5. Una sola unidad => el fichero se llama documento.md y NO se emite ninguna
//     advertencia por ese hecho. Esto es literal en el enunciado: "el proceso
//     no falla ni emite advertencias por el solo hecho de contener una unica
//     unidad".
//
// El punto 2 merece justificacion: elegir siempre H1 partiria en una sola
// unidad el documento tipico que tiene un unico titulo y varios "##" (fallando
// la condicion de documento estructurado), y elegir siempre H2 dejaria fuera un
// documento organizado solo con H1. Cortar por "el nivel mas alto que de
// verdad se repite" resuelve los dos casos con una sola regla y sin ramas
// especiales.
func Split(doc *docmodel.Document, maxUnits int) (*Result, error) {
	headings := doc.Headings()
	level := chooseSplitLevel(headings)

	res := &Result{SplitLevel: level, AnchorToFile: map[string]string{}}
	dedupeFiles := slug.NewDeduper()

	// Caso sin cortes: una unica unidad con todo el documento.
	if level == 0 {
		title := doc.Title
		if title == "" {
			title = "Documento"
		}
		u := Unit{
			Order:     1,
			Title:     title,
			Slug:      SingleUnitName,
			Filename:  SingleUnitName + ".md",
			Anchor:    slug.Make(title),
			Blocks:    doc.Blocks,
			Synthetic: true,
		}
		res.Units = []Unit{u}
		mapAnchors(res, u)
		return res, nil
	}

	// Puntos de corte: indices de bloque de los encabezados del nivel elegido.
	var cuts []int
	for _, h := range headings {
		if h.Level == level {
			cuts = append(cuts, h.BlockIndex)
		}
	}

	order := 0
	dedupeSlugs := slug.NewDeduper()

	// Preambulo: contenido antes del primer corte.
	if cuts[0] > 0 {
		blocks := doc.Blocks[:cuts[0]]
		if hasProseContent(blocks) {
			title := doc.Title
			if title == "" {
				title = "Introduccion"
			}
			order++
			s := dedupeSlugs.Unique(slug.Make(title))
			u := Unit{
				Order:     order,
				Title:     title,
				Slug:      s,
				Filename:  dedupeFiles.Unique(fmt.Sprintf("%02d-%s", order, s)) + ".md",
				Anchor:    s,
				Blocks:    blocks,
				Synthetic: true,
			}
			res.Units = append(res.Units, u)
			mapAnchors(res, u)
		}
	}

	for i, start := range cuts {
		end := len(doc.Blocks)
		if i+1 < len(cuts) {
			end = cuts[i+1]
		}
		blocks := doc.Blocks[start:end]

		title := doc.Blocks[start].Text
		if title == "" {
			title = fmt.Sprintf("Seccion %d", i+1)
		}

		order++
		if maxUnits > 0 && order > maxUnits {
			return nil, fmt.Errorf("el documento produce mas de %d unidades", maxUnits)
		}

		s := dedupeSlugs.Unique(slug.Make(title))
		u := Unit{
			Order:    order,
			Title:    title,
			Slug:     s,
			Filename: dedupeFiles.Unique(fmt.Sprintf("%02d-%s", order, s)) + ".md",
			Anchor:   s,
			Blocks:   blocks,
		}
		res.Units = append(res.Units, u)
		mapAnchors(res, u)
	}

	// Un documento que solo contiene un encabezado del nivel de corte y nada
	// mas produce una unidad; se le da el nombre canonico para que el caso de
	// documento breve sea indistinguible del caso sin encabezados.
	if len(res.Units) == 1 {
		res.Units[0].Slug = SingleUnitName
		res.Units[0].Filename = SingleUnitName + ".md"
		res.AnchorToFile = map[string]string{}
		mapAnchors(res, res.Units[0])
	}

	return res, nil
}

// chooseSplitLevel implementa el punto 2 de la regla.
func chooseSplitLevel(headings []docmodel.HeadingRef) int {
	counts := map[int]int{}
	minLevel := 0
	for _, h := range headings {
		if h.Level > 3 {
			continue // H4+ nunca parte: seria trocear demasiado fino
		}
		counts[h.Level]++
		if minLevel == 0 || h.Level < minLevel {
			minLevel = h.Level
		}
	}
	if minLevel == 0 {
		return 0 // sin encabezados utilizables
	}
	for lvl := 1; lvl <= 3; lvl++ {
		if counts[lvl] >= 2 {
			return lvl
		}
	}
	return minLevel
}

func mapAnchors(r *Result, u Unit) {
	for _, b := range u.Blocks {
		if b.Kind == docmodel.KindHeading && b.Text != "" {
			r.AnchorToFile[slug.Make(b.Text)] = u.Filename
		}
	}
	r.AnchorToFile[u.Anchor] = u.Filename
}

// hasProseContent indica si un tramo de bloques contiene prosa propia, y no
// solo encabezados.
//
// Se usa para decidir si el preambulo merece ser una unidad. Un documento que
// empieza con "# Titulo" seguido directamente de "## Capitulo 1" tiene un
// preambulo formado unicamente por el encabezado del titulo: convertirlo en
// concepto produciria un fichero sin cuerpo, y el titulo ya queda registrado en
// el front-matter y en el titulo de index.md. En cambio, en cuanto hay un
// parrafo, una lista o una imagen antes del primer corte, ese contenido SI debe
// preservarse como unidad: no hacerlo lo eliminaria del bundle en silencio, que
// es el fallo mas facil de cometer en esta parte.
func hasProseContent(blocks []docmodel.Block) bool {
	for _, b := range blocks {
		switch b.Kind {
		case docmodel.KindParagraph, docmodel.KindList, docmodel.KindCode,
			docmodel.KindQuote, docmodel.KindTable, docmodel.KindImage, docmodel.KindRaw:
			if b.Text != "" || len(b.Items) > 0 || len(b.Rows) > 0 || b.Image != nil {
				return true
			}
		}
	}
	return false
}
