package segment_test

import (
	"testing"

	"github.com/uniandes-isis4426/okfp/internal/okf/docmodel"
	"github.com/uniandes-isis4426/okfp/internal/okf/segment"
)

func h(level int, text string) docmodel.Block {
	return docmodel.Block{Kind: docmodel.KindHeading, Level: level, Text: text}
}

func p(text string) docmodel.Block {
	return docmodel.Block{Kind: docmodel.KindParagraph, Text: text}
}

// TestSplit cubre la regla de segmentacion caso por caso. Es la prueba que
// sostiene las condiciones de documento breve y documento estructurado.
func TestSplit(t *testing.T) {
	tests := []struct {
		name       string
		doc        *docmodel.Document
		wantUnits  int
		wantLevel  int
		wantFirst  string
		wantTitles []string
	}{
		{
			name: "sin encabezados produce una sola unidad llamada documento.md",
			doc: &docmodel.Document{Blocks: []docmodel.Block{
				p("Un parrafo suelto."), p("Otro parrafo."),
			}},
			wantUnits: 1,
			wantLevel: 0,
			wantFirst: "documento.md",
		},
		{
			name: "un unico H1 con cuerpo sigue siendo una sola unidad",
			doc: &docmodel.Document{Title: "Guia breve", Blocks: []docmodel.Block{
				h(1, "Guia breve"), p("Contenido."),
			}},
			wantUnits: 1,
			wantLevel: 1,
			wantFirst: "documento.md",
		},
		{
			name: "H1 con varios H2 corta por H2 y conserva el preambulo",
			doc: &docmodel.Document{Title: "Manual", Blocks: []docmodel.Block{
				h(1, "Manual"),
				p("Introduccion que no pertenece a ningun capitulo."),
				h(2, "Instalacion"), p("Pasos."),
				h(2, "Uso"), p("Como se usa."),
			}},
			wantUnits:  3, // preambulo + 2 capitulos
			wantLevel:  2,
			wantTitles: []string{"Manual", "Instalacion", "Uso"},
		},
		{
			name: "varios H1 cortan por H1",
			doc: &docmodel.Document{Blocks: []docmodel.Block{
				h(1, "Capitulo uno"), p("A"),
				h(1, "Capitulo dos"), p("B"),
				h(1, "Capitulo tres"), p("C"),
			}},
			wantUnits:  3,
			wantLevel:  1,
			wantTitles: []string{"Capitulo uno", "Capitulo dos", "Capitulo tres"},
		},
		{
			name: "los H3 anidados no parten la unidad de nivel 2",
			doc: &docmodel.Document{Blocks: []docmodel.Block{
				h(2, "Alfa"), h(3, "Alfa uno"), p("x"), h(3, "Alfa dos"), p("y"),
				h(2, "Beta"), p("z"),
			}},
			wantUnits:  2,
			wantLevel:  2,
			wantTitles: []string{"Alfa", "Beta"},
		},
		{
			name: "titulos que colisionan producen ficheros distintos",
			doc: &docmodel.Document{Blocks: []docmodel.Block{
				h(2, "Introduccion"), p("a"),
				h(2, "Introduccion"), p("b"),
			}},
			wantUnits: 2,
			wantLevel: 2,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			res, err := segment.Split(tc.doc, 100)
			if err != nil {
				t.Fatalf("Split: %v", err)
			}
			if len(res.Units) != tc.wantUnits {
				t.Fatalf("unidades = %d, se esperaban %d", len(res.Units), tc.wantUnits)
			}
			if res.SplitLevel != tc.wantLevel {
				t.Errorf("nivel de corte = %d, se esperaba %d", res.SplitLevel, tc.wantLevel)
			}
			if tc.wantFirst != "" && res.Units[0].Filename != tc.wantFirst {
				t.Errorf("primer fichero = %q, se esperaba %q", res.Units[0].Filename, tc.wantFirst)
			}
			for i, want := range tc.wantTitles {
				if res.Units[i].Title != want {
					t.Errorf("unidad %d: titulo = %q, se esperaba %q", i, res.Units[i].Title, want)
				}
			}
			// El orden debe ser estrictamente creciente y empezar en 1: es lo
			// que despues verifica el validador contra el indice.
			for i, u := range res.Units {
				if u.Order != i+1 {
					t.Errorf("unidad %d: order = %d, se esperaba %d", i, u.Order, i+1)
				}
			}
			// Los nombres de fichero deben ser unicos: dos secciones con el
			// mismo titulo no pueden sobreescribirse.
			seen := map[string]bool{}
			for _, u := range res.Units {
				if seen[u.Filename] {
					t.Errorf("nombre de fichero duplicado: %s", u.Filename)
				}
				seen[u.Filename] = true
			}
		})
	}
}

// TestNoContentIsLost comprueba que el contenido anterior al primer corte no
// desaparece. Es el fallo silencioso mas facil de cometer aqui: cortar solo por
// los encabezados dejaria fuera la introduccion del documento tipico.
func TestNoContentIsLost(t *testing.T) {
	doc := &docmodel.Document{Title: "T", Blocks: []docmodel.Block{
		h(1, "T"),
		p("PREAMBULO IMPORTANTE"),
		h(2, "Uno"), p("a"),
		h(2, "Dos"), p("b"),
	}}

	res, err := segment.Split(doc, 100)
	if err != nil {
		t.Fatalf("Split: %v", err)
	}

	var total int
	found := false
	for _, u := range res.Units {
		for _, b := range u.Blocks {
			total++
			if b.Text == "PREAMBULO IMPORTANTE" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("el preambulo anterior al primer corte se perdio")
	}
	if total != len(doc.Blocks) {
		t.Errorf("se repartieron %d bloques de %d", total, len(doc.Blocks))
	}
}
