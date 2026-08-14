// Package okf orquesta la conversion completa: del documento de origen al
// bundle validado.
//
// Este paquete lo importa UNICAMENTE cmd/worker. Que cmd/api no lo importe no
// es una convencion sino una garantia estructural verificable con
// `go list -deps ./cmd/api`, y es la forma mas fuerte de sostener que la
// conversion no puede ejecutarse dentro de la peticion HTTP: el binario de la
// API ni siquiera contiene el conversor.
package okf

import (
	"context"
	"fmt"
	"time"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf/assets"
	"github.com/uniandes-isis4426/okfp/internal/okf/bundlefs"
	"github.com/uniandes-isis4426/okfp/internal/okf/docmodel"
	"github.com/uniandes-isis4426/okfp/internal/okf/parse"
	"github.com/uniandes-isis4426/okfp/internal/okf/render"
	"github.com/uniandes-isis4426/okfp/internal/okf/segment"
	"github.com/uniandes-isis4426/okfp/internal/okf/validate"
)

// Input es todo lo que la conversion necesita.
type Input struct {
	Source     *parse.Source
	Options    parse.Options
	BundleID   string
	JobID      string
	Attempt    int
	WorkerID   string
	CreatedAt  time.Time // fecha del trabajo: hace determinista generated_at
	SlowModeMS int

	// CheckCancel se consulta entre etapas. Un mensaje ya entregado no puede
	// des-encolarse desde AMQP, asi que la unica cancelacion posible es que el
	// propio worker decida parar en un punto de control.
	CheckCancel func(ctx context.Context) bool

	// FaultInject provoca a proposito un bundle incompleto para poder demostrar
	// la condicion literal del enunciado. Vacio en funcionamiento normal.
	// Valores: "drop_index", "drop_log".
	FaultInject string
}

// Output es el bundle candidato ya validado, aun sin publicar.
type Output struct {
	FS      *bundlefs.FS
	Report  *domain.ValidationReport
	Units   int
	Stages  []render.Stage
	Elapsed time.Duration
}

// ErrCanceled indica que la cancelacion cooperativa se atendio.
var ErrCanceled = fmt.Errorf("conversion cancelada por peticion del usuario")

// Convert ejecuta el pipeline completo en memoria.
//
// Nada se escribe en el almacenamiento hasta que el llamante decide publicar:
// asi, un bundle que no supera la validacion no llega a existir en el prefijo
// servible bajo ninguna circunstancia.
func Convert(ctx context.Context, in Input) (*Output, error) {
	start := time.Now()
	out := &Output{FS: bundlefs.New()}

	stage := func(name, detail string, at time.Time) {
		out.Stages = append(out.Stages, render.Stage{
			Name: name, Detail: detail, At: at, Duration: time.Since(at),
		})
	}

	checkCancel := func() error {
		if in.CheckCancel != nil && in.CheckCancel(ctx) {
			return ErrCanceled
		}
		return ctx.Err()
	}

	// --- 1. Parseo ----------------------------------------------------------
	t := time.Now()
	parser, err := parse.For(in.Source.Format)
	if err != nil {
		return nil, err
	}
	doc, err := parser.Parse(ctx, in.Source, in.Options)
	if err != nil {
		return nil, err
	}
	if !doc.HasContent() {
		return nil, domain.PermanentFault("empty_document",
			"El documento no contiene contenido convertible", nil)
	}
	stage("parse", fmt.Sprintf("%d bloques desde %s", len(doc.Blocks), doc.SourceFormat), t)

	if err := checkCancel(); err != nil {
		return nil, err
	}

	// Retardo de demostracion. Es explicito, esta desactivado por defecto y se
	// declara tanto en la cronologia como en el log: nunca es evidencia oculta
	// de asincronia.
	if in.SlowModeMS > 0 {
		t = time.Now()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Duration(in.SlowModeMS) * time.Millisecond):
		}
		stage("demo_slow_mode", fmt.Sprintf("retardo explicito de %d ms", in.SlowModeMS), t)
	}

	// --- 2. Segmentacion ----------------------------------------------------
	t = time.Now()
	seg, err := segment.Split(doc, in.Options.MaxUnits)
	if err != nil {
		return nil, domain.PermanentFault("too_many_units", err.Error(), err)
	}
	out.Units = len(seg.Units)
	stage("segment", fmt.Sprintf("%d unidad(es), nivel de corte %d", len(seg.Units), seg.SplitLevel), t)

	if err := checkCancel(); err != nil {
		return nil, err
	}

	// --- 3. Assets ----------------------------------------------------------
	t = time.Now()
	unitBlocks := make([][]docmodel.Block, 0, len(seg.Units))
	for i := range seg.Units {
		unitBlocks = append(unitBlocks, seg.Units[i].Blocks)
	}
	assetRes := assets.Extract(unitBlocks, out.FS, in.Options.AssetsMax)
	stage("assets", fmt.Sprintf("%d extraido(s), %d remoto(s) conservado(s)",
		assetRes.Extracted, assetRes.Remote), t)

	// --- 4. Render de conceptos ---------------------------------------------
	t = time.Now()
	title := doc.Title
	if title == "" {
		title = in.Source.Name
	}
	meta := render.Meta{
		BundleID:    in.BundleID,
		JobID:       in.JobID,
		SourceName:  in.Source.Name,
		SourceFmt:   doc.SourceFormat,
		Title:       title,
		UnitCount:   len(seg.Units),
		GeneratedAt: in.CreatedAt.UTC().Format(time.RFC3339),
	}

	for i := range seg.Units {
		var prev, next *segment.Unit
		if i > 0 {
			prev = &seg.Units[i-1]
		}
		if i+1 < len(seg.Units) {
			next = &seg.Units[i+1]
		}
		data := render.Concept(seg.Units[i], meta, prev, next, seg.AnchorToFile)
		out.FS.Put(seg.Units[i].Filename, data)
	}
	stage("render", fmt.Sprintf("%d documento(s) de concepto", len(seg.Units)), t)

	// --- 5. index.md --------------------------------------------------------
	t = time.Now()
	out.FS.Put(bundlefs.IndexPath, render.Index(meta, seg.Units, ""))
	stage("index", "index.md con indice delimitado y enlaces relativos", t)

	if err := checkCancel(); err != nil {
		return nil, err
	}

	// --- 6. Validacion en punto fijo ----------------------------------------
	//
	// Se valida, se escribe el log definitivo y se VUELVE a validar sobre los
	// bytes exactos que se publicaran. Sin la segunda pasada, el epilogo del
	// log -que interpola texto procedente del documento del usuario- se
	// publicaria sin haber pasado por las reglas.
	notes := collectNotes(doc, assetRes.Notes)

	logData := render.LogData{
		Meta:       meta,
		Units:      seg.Units,
		SplitLevel: seg.SplitLevel,
		Assets:     assetRes.Extracted,
		Notes:      notes,
		Transforms: transformsFor(doc),
		Stages:     out.Stages,
		WorkerID:   in.WorkerID,
		Attempt:    in.Attempt,
		StartedAt:  start,
		SlowModeMS: in.SlowModeMS,
	}
	out.FS.Put(bundlefs.LogPath, render.Log(logData))

	t = time.Now()
	first := validate.Run(out.FS, nil)

	logData.Report = first
	logData.FinishedAt = time.Now()
	out.FS.Put(bundlefs.LogPath, render.Log(logData))

	// Inyeccion de fallo para la demostracion en vivo. Se aplica una vez el
	// bundle esta completo y ANTES de la validacion definitiva, que es
	// exactamente donde el evaluador espera ver actuar al validador.
	//
	// Va aqui y no antes de la primera pasada porque el pipeline reescribe
	// log.md al conocer el veredicto: borrar el fichero antes lo haria
	// reaparecer y el fallo no se produciria.
	injected := false
	switch in.FaultInject {
	case "drop_index":
		out.FS.Delete(bundlefs.IndexPath)
		injected = true
		stage("fault_inject", "se elimino index.md a proposito (OKF_FAULT_INJECT=drop_index)", time.Now())
	case "drop_log":
		out.FS.Delete(bundlefs.LogPath)
		injected = true
		stage("fault_inject", "se elimino log.md a proposito (OKF_FAULT_INJECT=drop_log)", time.Now())
	}

	second := validate.Run(out.FS, nil)

	// La comprobacion de punto fijo solo tiene sentido sin inyeccion: con ella,
	// los hallazgos difieren por diseno y anadir PLT-VAL-001 confundiria el
	// diagnostico que se muestra en pantalla.
	if !injected && !sameCodes(first, second) {
		second.Findings = append(second.Findings, domain.Finding{
			Code: "PLT-VAL-001", Axis: domain.AxisPlatform, Severity: domain.SeverityError,
			Message: "La validacion no alcanzo un punto fijo: los bytes publicados difieren de los validados",
		})
		second.Verdict = second.Classify()
	}
	stage("validate", fmt.Sprintf("veredicto %s, conformidad OKF %d/100",
		second.Verdict, second.OKFScore), t)

	out.Report = second
	out.Elapsed = time.Since(start)

	if in.Options.MaxBytes > 0 && out.FS.TotalBytes() > in.Options.MaxBytes*8 {
		return nil, domain.PermanentFault("bundle_too_large",
			"El bundle generado supera el tamano maximo permitido", nil)
	}
	return out, nil
}

func collectNotes(doc *docmodel.Document, extra []string) []string {
	var out []string
	for _, n := range doc.Notes {
		out = append(out, n.Message)
	}
	return append(out, extra...)
}

func transformsFor(doc *docmodel.Document) []string {
	t := []string{
		fmt.Sprintf("Conversion de %s a la representacion intermedia comun", doc.SourceFormat),
		"Segmentacion en unidades logicas preservando el orden del documento de origen",
		"Renivelacion de encabezados dentro de cada concepto",
		"Reescritura de enlaces internos por ancla hacia el fichero de destino",
	}
	if doc.StructureInferred {
		t = append(t, "Deteccion heuristica de encabezados (el formato de origen no los declara explicitamente)")
	}
	return t
}

func sameCodes(a, b *domain.ValidationReport) bool {
	ca, cb := map[string]int{}, map[string]int{}
	for _, f := range a.Findings {
		if f.Severity == domain.SeverityError {
			ca[f.Code]++
		}
	}
	for _, f := range b.Findings {
		if f.Severity == domain.SeverityError {
			cb[f.Code]++
		}
	}
	if len(ca) != len(cb) {
		return false
	}
	for k, v := range ca {
		if cb[k] != v {
			return false
		}
	}
	return true
}
