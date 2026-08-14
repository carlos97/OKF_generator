package okf_test

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf"
	"github.com/uniandes-isis4426/okfp/internal/okf/bundlefs"
	"github.com/uniandes-isis4426/okfp/internal/okf/parse"
	"github.com/uniandes-isis4426/okfp/internal/okf/render"
)

func convert(t *testing.T, name, body string, format domain.Format) *okf.Output {
	t.Helper()
	r := bytes.NewReader([]byte(body))
	out, err := okf.Convert(context.Background(), okf.Input{
		Source: &parse.Source{R: r, Size: int64(len(body)), Name: name, Format: format},
		Options: parse.Options{
			MaxBytes: 5 << 20, MaxUnits: 500, AssetsMax: 4 << 20,
			ZipMaxEntries: 2000, ZipMaxRatio: 100,
		},
		BundleID:  "11111111-1111-1111-1111-111111111111",
		JobID:     "22222222-2222-2222-2222-222222222222",
		Attempt:   1,
		WorkerID:  "worker-test",
		CreatedAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	return out
}

// TestC2_DocumentoBreveSinAdvertencias es la condicion verificable literal del
// enunciado: "un documento corto sin divisiones produce index.md, log.md y un
// unico concepto. El proceso no falla ni emite advertencias por el solo hecho
// de contener una unica unidad".
//
// La parte que se suele incumplir es la ultima: es tentador advertir "no se
// detectaron encabezados", y eso hace fallar la condicion en camara.
func TestC2_DocumentoBreveSinAdvertencias(t *testing.T) {
	out := convert(t, "breve.md", strings.TrimSpace(`
# Nota breve

Este documento no tiene divisiones. Es un unico bloque de prosa que debe
convertirse en un bundle completo con un solo concepto.

Segundo parrafo para dar algo de cuerpo al documento.
`)+"\n", domain.FormatMarkdown)

	// Estructura minima exacta.
	for _, want := range []string{bundlefs.IndexPath, bundlefs.LogPath, "documento.md"} {
		if !out.FS.Has(want) {
			t.Errorf("falta %s en el bundle", want)
		}
	}
	if got := len(out.FS.Concepts()); got != 1 {
		t.Errorf("conceptos = %d, se esperaba 1", got)
	}

	// Ni un solo aviso por tener una unica unidad.
	if w := out.Report.Warnings(); len(w) != 0 {
		for _, f := range w {
			t.Errorf("advertencia inesperada %s: %s", f.Code, f.Message)
		}
	}
	if out.Report.Verdict != domain.ResultValid {
		t.Errorf("veredicto = %s, se esperaba %s", out.Report.Verdict, domain.ResultValid)
	}

	// El log debe decir explicitamente que esto es normal.
	logData, _ := out.FS.Get(bundlefs.LogPath)
	if !strings.Contains(string(logData), "no constituye una advertencia") {
		t.Error("log.md no explica que una unidad unica es un resultado esperado")
	}
}

// TestC3_DocumentoEstructuradoEnOrden comprueba que un documento con varias
// secciones produce un concepto por unidad, enlazados EN ORDEN desde el indice.
func TestC3_DocumentoEstructuradoEnOrden(t *testing.T) {
	out := convert(t, "manual.md", strings.TrimSpace(`
# Manual de usuario

Introduccion general del manual.

## Requisitos previos

Necesita Docker instalado.

## Instalacion

Ejecute el comando de arranque.

## Uso diario

Suba un documento y descargue el bundle.
`)+"\n", domain.FormatMarkdown)

	concepts := out.FS.Concepts()
	if len(concepts) != 4 { // preambulo + 3 secciones
		t.Fatalf("conceptos = %d, se esperaban 4", len(concepts))
	}

	index, _ := out.FS.Get(bundlefs.IndexPath)
	idx := string(index)

	// Los enlaces deben aparecer dentro del bloque delimitado y en orden.
	start := strings.Index(idx, render.TOCOpen)
	end := strings.Index(idx, render.TOCClose)
	if start < 0 || end < 0 {
		t.Fatal("index.md no contiene el bloque de indice delimitado")
	}
	toc := idx[start:end]

	prev := -1
	for _, c := range concepts {
		pos := strings.Index(toc, "("+"./"+c.Path+")")
		if pos < 0 {
			t.Fatalf("el indice no enlaza %s", c.Path)
		}
		if pos < prev {
			t.Errorf("el enlace a %s aparece fuera de orden en el indice", c.Path)
		}
		prev = pos
	}

	if out.Report.Verdict == domain.ResultInvalid {
		for _, f := range out.Report.Errors() {
			t.Errorf("error de validacion %s: %s", f.Code, f.Message)
		}
	}
}

// TestC4_BundleIncompletoNoEsPublicable: si falta index.md o log.md, la
// validacion falla y el bundle no puede publicarse.
func TestC4_BundleIncompletoNoEsPublicable(t *testing.T) {
	for _, missing := range []string{bundlefs.IndexPath, bundlefs.LogPath} {
		t.Run("falta "+missing, func(t *testing.T) {
			out := convert(t, "manual.md",
				"# T\n\n## A\n\ntexto a\n\n## B\n\ntexto b\n", domain.FormatMarkdown)

			out.FS.Delete(missing)
			report := validateAgain(out)

			if report.Publishable() {
				t.Fatalf("un bundle sin %s no debe ser publicable", missing)
			}
			if report.Verdict != domain.ResultInvalid {
				t.Errorf("veredicto = %s, se esperaba %s", report.Verdict, domain.ResultInvalid)
			}
		})
	}
}

// TestDeterminismo: convertir dos veces el mismo documento produce bundles
// identicos salvo el bloque de cronologia, que lleva marcas de tiempo y esta
// delimitado a proposito para poder excluirlo.
func TestDeterminismo(t *testing.T) {
	const src = "# T\n\n## A\n\ntexto a\n\n## B\n\ntexto b\n"

	a := convert(t, "doc.md", src, domain.FormatMarkdown)
	time.Sleep(5 * time.Millisecond)
	b := convert(t, "doc.md", src, domain.FormatMarkdown)

	fa, fb := a.FS.Files(), b.FS.Files()
	if len(fa) != len(fb) {
		t.Fatalf("numero de ficheros distinto: %d vs %d", len(fa), len(fb))
	}
	for i := range fa {
		if fa[i].Path != fb[i].Path {
			t.Fatalf("orden de ficheros distinto: %s vs %s", fa[i].Path, fb[i].Path)
		}
		x := render.StripTimeline(fa[i].Data)
		y := render.StripTimeline(fb[i].Data)
		if !bytes.Equal(x, y) {
			t.Errorf("%s difiere entre dos conversiones del mismo documento", fa[i].Path)
		}
	}
}

// TestTextoPlanoConEncabezados verifica el segundo formato de entrada.
func TestTextoPlanoConEncabezados(t *testing.T) {
	out := convert(t, "notas.txt", strings.TrimSpace(`
INFORME ANUAL

Texto introductorio del informe.

1. RESULTADOS

Los resultados fueron satisfactorios.

2. CONCLUSIONES

Se concluye el ejercicio.
`)+"\n", domain.FormatText)

	if len(out.FS.Concepts()) < 2 {
		t.Errorf("conceptos = %d, se esperaban al menos 2", len(out.FS.Concepts()))
	}
	if out.Report.Verdict == domain.ResultInvalid {
		for _, f := range out.Report.Errors() {
			t.Errorf("error %s: %s", f.Code, f.Message)
		}
	}
}

// TestHTMLConImagenEmbebida verifica la extraccion de assets y que el script se
// descarta.
func TestHTMLConImagenEmbebida(t *testing.T) {
	const png = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg=="
	html := `<html><head><title>Con imagen</title></head><body>
<h1>Con imagen</h1>
<script>alert(1)</script>
<h2>Primera</h2><p>Texto uno</p>
<h2>Segunda</h2><p>Texto dos</p><img src="` + png + `" alt="pixel">
</body></html>`

	out := convert(t, "pagina.html", html, domain.FormatHTML)

	if len(out.FS.Assets()) != 1 {
		t.Errorf("assets = %d, se esperaba 1", len(out.FS.Assets()))
	}
	for _, f := range out.FS.Files() {
		if strings.Contains(string(f.Data), "alert(1)") {
			t.Errorf("%s conserva el script del documento de origen", f.Path)
		}
	}
	if len(out.FS.Concepts()) != 2 {
		t.Errorf("conceptos = %d, se esperaban 2", len(out.FS.Concepts()))
	}
}
