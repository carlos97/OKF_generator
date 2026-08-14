package okf_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf"
	"github.com/uniandes-isis4426/okfp/internal/okf/parse"
)

// TestTestdata convierte los documentos de demostracion y comprueba que cada uno
// produce el veredicto que el guion del video promete.
//
// Sin este test, los ficheros de testdata/ son una promesa sin verificar: el dia
// de la sustentacion se descubriria que el documento "invalido" en realidad se
// publica y la condicion de bundle incompleto no se puede demostrar.
func TestTestdata(t *testing.T) {
	root := filepath.Join("..", "..", "testdata")

	cases := []struct {
		file        string
		format      domain.Format
		wantVerdict domain.ResultClass
		minConcepts int
		wantAssets  int
		note        string
	}{
		{
			file: "01-breve.md", format: domain.FormatMarkdown,
			wantVerdict: domain.ResultValid, minConcepts: 1,
			note: "documento breve sin divisiones: valido y SIN advertencias",
		},
		{
			file: "02-capitulos.md", format: domain.FormatMarkdown,
			wantVerdict: domain.ResultValid, minConcepts: 5,
			note: "documento estructurado: un concepto por unidad, en orden",
		},
		{
			// Este documento es el ejemplo natural del veredicto INTERMEDIO: la
			// imagen "no-existe.png" que incluye a proposito no resuelve dentro
			// del bundle, y eso es una advertencia (no un error), porque proviene
			// del contenido del usuario y no de la estructura del bundle.
			// Invalidarlo seria rechazar un documento legitimo.
			file: "03-con-imagenes.html", format: domain.FormatHTML,
			wantVerdict: domain.ResultValidWithWarnings, minConcepts: 3, wantAssets: 1,
			note: "HTML con recurso embebido extraido a assets/ y una referencia rota que solo advierte",
		},
		{
			file: "04-invalido.txt", format: domain.FormatText,
			wantVerdict: domain.ResultInvalid,
			note:        "contenido que no es UTF-8 valido: no debe publicarse",
		},
	}

	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, tc.file))
			if err != nil {
				t.Skipf("no se encontro %s: %v", tc.file, err)
			}

			out, err := okf.Convert(context.Background(), okf.Input{
				Source: &parse.Source{
					R: bytes.NewReader(raw), Size: int64(len(raw)),
					Name: tc.file, Format: tc.format,
				},
				Options: parse.Options{
					MaxBytes: 5 << 20, MaxUnits: 500, AssetsMax: 4 << 20,
					ZipMaxEntries: 2000, ZipMaxRatio: 100,
				},
				BundleID:  "33333333-3333-3333-3333-333333333333",
				JobID:     "44444444-4444-4444-4444-444444444444",
				Attempt:   1,
				WorkerID:  "worker-test",
				CreatedAt: time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatalf("Convert(%s): %v", tc.file, err)
			}

			if out.Report.Verdict != tc.wantVerdict {
				t.Errorf("veredicto = %s, se esperaba %s (%s)",
					out.Report.Verdict, tc.wantVerdict, tc.note)
				for _, f := range out.Report.Findings {
					if f.Axis == domain.AxisPlatform {
						t.Logf("  hallazgo %s [%s] %s (%s)", f.Code, f.Severity, f.Message, f.Path)
					}
				}
			}

			if n := len(out.FS.Concepts()); n < tc.minConcepts {
				t.Errorf("conceptos = %d, se esperaban al menos %d", n, tc.minConcepts)
			}
			if tc.wantAssets > 0 {
				if n := len(out.FS.Assets()); n != tc.wantAssets {
					t.Errorf("assets = %d, se esperaban %d", n, tc.wantAssets)
				}
			}

			// El bundle invalido no debe poder publicarse bajo ningun concepto.
			if tc.wantVerdict == domain.ResultInvalid && out.Report.Publishable() {
				t.Error("un bundle invalido nunca debe ser publicable")
			}
		})
	}
}
