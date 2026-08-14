package render

import (
	"fmt"
	"strings"
	"time"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf/segment"
)

// Centinelas de la cronologia. Todo lo que hay entre ellos lleva marcas de
// tiempo y por tanto NO es determinista; el resto del fichero si lo es.
//
// Esta separacion es lo que permite afirmar con honestidad que "el contenido
// del bundle es una funcion determinista del documento y del trabajo": los
// tests golden comparan el bundle completo excluyendo este bloque, de modo que
// lo que se afirma es exactamente lo que se verifica. Sin la separacion, el
// argumento seria refutable en vivo simplemente abriendo dos log.md.
const (
	TimelineOpen  = "<!-- okf:timeline -->"
	TimelineClose = "<!-- /okf:timeline -->"
)

// Stage es una etapa registrada del pipeline.
type Stage struct {
	Name     string
	Detail   string
	Duration time.Duration
	At       time.Time
}

// LogData es todo lo que log.md necesita.
type LogData struct {
	Meta       Meta
	Units      []segment.Unit
	SplitLevel int
	Assets     int
	Notes      []string
	Transforms []string
	Stages     []Stage
	Report     *domain.ValidationReport
	WorkerID   string
	Attempt    int
	StartedAt  time.Time
	FinishedAt time.Time
	SlowModeMS int
}

// Log renderiza log.md.
//
// El fichero se emite en dos pasadas: primero con el veredicto en estado
// pendiente (para poder validar la estructura), y despues con el veredicto
// definitivo. La segunda pasada se vuelve a validar sobre los bytes exactos que
// se publicaran, de modo que lo validado y lo publicado son lo mismo.
func Log(d LogData) []byte {
	var b strings.Builder

	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("okf_version: %q\n", OKFVersion))
	b.WriteString("type: \"log\"\n")
	b.WriteString(fmt.Sprintf("bundle_id: %q\n", d.Meta.BundleID))
	b.WriteString(fmt.Sprintf("job_id: %q\n", d.Meta.JobID))
	b.WriteString("---\n\n")

	b.WriteString("# Registro de conversion\n\n")
	b.WriteString(fmt.Sprintf("Documento de origen: `%s` (formato detectado: **%s**).\n\n",
		sanitizeInline(d.Meta.SourceName), sanitizeInline(d.Meta.SourceFmt)))

	// --- Unidades detectadas (determinista) ---------------------------------
	b.WriteString("## Unidades detectadas\n\n")
	// La redaccion depende del NUMERO DE UNIDADES, no del nivel de corte: un
	// documento con un unico "# Titulo" tiene nivel de corte 1 y sin embargo es
	// el caso de documento breve sin divisiones. Decidir por el nivel de corte
	// haria que ese caso se describiera como si estuviera segmentado.
	if len(d.Units) <= 1 {
		b.WriteString("El documento no presenta divisiones estructurales, por lo que se genera " +
			"una unica unidad de concepto. Este es un resultado esperado y no constituye una advertencia.\n\n")
	} else {
		b.WriteString(fmt.Sprintf("Nivel de corte aplicado: encabezados de nivel %d "+
			"(el nivel mas alto que aparece al menos dos veces en el documento).\n\n", d.SplitLevel))
	}
	b.WriteString("| # | Titulo | Fichero |\n| --- | --- | --- |\n")
	for _, u := range d.Units {
		b.WriteString(fmt.Sprintf("| %d | %s | `%s` |\n", u.Order, sanitizeInline(u.Title), u.Filename))
	}
	b.WriteString(fmt.Sprintf("\nTotal: **%d** unidad(es).\n\n", len(d.Units)))

	// --- Transformaciones (determinista) ------------------------------------
	b.WriteString("## Transformaciones aplicadas\n\n")
	transforms := append([]string{}, d.Transforms...)
	transforms = append(transforms,
		fmt.Sprintf("Normalizacion de fin de linea a LF"),
		fmt.Sprintf("Generacion de nombres de fichero deterministas y seguros (ASCII, kebab-case)"),
		fmt.Sprintf("Generacion de index.md con indice delimitado y enlaces relativos"),
		fmt.Sprintf("Generacion de bloques de navegacion anterior/indice/siguiente en cada concepto"),
	)
	if d.Assets > 0 {
		transforms = append(transforms,
			fmt.Sprintf("Extraccion de %d recurso(s) a assets/, nombrados por hash de contenido", d.Assets))
	}
	for _, t := range transforms {
		b.WriteString("- " + sanitizeInline(t) + "\n")
	}
	b.WriteString("\n")

	// --- Validaciones (determinista) ----------------------------------------
	b.WriteString("## Validaciones\n\n")
	if d.Report == nil {
		b.WriteString("_pendiente_\n\n")
	} else {
		b.WriteString(fmt.Sprintf("Se evaluaron **%d** reglas de validez de plataforma y conformidad OKF.\n\n",
			d.Report.RulesEvaluated))

		errs := d.Report.Errors()
		warns := d.Report.Warnings()

		if len(errs) == 0 {
			b.WriteString("Todas las reglas obligatorias se superaron:\n\n")
			b.WriteString("- Existe `index.md` en la raiz\n")
			b.WriteString("- Existe `log.md` en la raiz\n")
			b.WriteString("- Existe al menos un documento de concepto\n")
			b.WriteString("- Todos los enlaces del indice resuelven a ficheros existentes del bundle\n")
			b.WriteString("- Todos los conceptos estan enlazados desde el indice y en el mismo orden\n")
			b.WriteString("- Todas las rutas son relativas y seguras\n")
			b.WriteString("- Todo el contenido es UTF-8 valido\n\n")
		} else {
			b.WriteString("### Errores que impiden la publicacion\n\n")
			b.WriteString("| Codigo | Fichero | Mensaje |\n| --- | --- | --- |\n")
			for _, f := range errs {
				b.WriteString(fmt.Sprintf("| `%s` | `%s` | %s |\n",
					f.Code, sanitizeInline(f.Path), sanitizeInline(f.Message)))
			}
			b.WriteString("\n")
		}

		if len(warns) > 0 {
			b.WriteString("### Advertencias\n\n")
			b.WriteString("| Codigo | Fichero | Mensaje |\n| --- | --- | --- |\n")
			for _, f := range warns {
				b.WriteString(fmt.Sprintf("| `%s` | `%s` | %s |\n",
					f.Code, sanitizeInline(f.Path), sanitizeInline(f.Message)))
			}
			b.WriteString("\n")
		}
	}

	// --- Notas del conversor -------------------------------------------------
	if len(d.Notes) > 0 {
		b.WriteString("## Observaciones del conversor\n\n")
		for _, n := range d.Notes {
			b.WriteString("- " + sanitizeInline(n) + "\n")
		}
		b.WriteString("\n")
	}

	// --- Veredicto (determinista) -------------------------------------------
	b.WriteString("## Veredicto\n\n")
	if d.Report == nil {
		b.WriteString("_pendiente_\n\n")
	} else {
		b.WriteString(fmt.Sprintf("- **Validez de plataforma**: `%s`\n", d.Report.Verdict))
		b.WriteString(fmt.Sprintf("- **Conformidad OKF**: %d/100 (grado %s)\n\n",
			d.Report.OKFScore, d.Report.OKFGrade))
		b.WriteString("La validez de plataforma es una puerta binaria: decide si el bundle se publica. " +
			"La conformidad OKF es una medida de calidad que nunca bloquea la publicacion. " +
			"Son dos ejes independientes y se reportan por separado.\n\n")
	}

	// --- Cronologia (NO determinista, delimitada) ---------------------------
	b.WriteString(TimelineOpen + "\n")
	b.WriteString("## Cronologia\n\n")
	if d.SlowModeMS > 0 {
		b.WriteString(fmt.Sprintf("> Nota: se aplico un retardo de demostracion de %d ms. "+
			"Este retardo es explicito y configurable; no forma parte del procesamiento real.\n\n", d.SlowModeMS))
	}
	b.WriteString(fmt.Sprintf("Worker: `%s` · Intento: %d\n\n", sanitizeInline(d.WorkerID), d.Attempt))
	b.WriteString("| Hora (UTC) | Etapa | Duracion | Detalle |\n| --- | --- | --- | --- |\n")
	for _, s := range d.Stages {
		b.WriteString(fmt.Sprintf("| %s | %s | %s | %s |\n",
			s.At.UTC().Format("15:04:05.000"), s.Name,
			s.Duration.Round(time.Millisecond), sanitizeInline(s.Detail)))
	}
	if !d.FinishedAt.IsZero() {
		b.WriteString(fmt.Sprintf("\nDuracion total: **%s**.\n",
			d.FinishedAt.Sub(d.StartedAt).Round(time.Millisecond)))
	}
	b.WriteString(TimelineClose + "\n")

	return []byte(b.String())
}

// sanitizeInline neutraliza texto de origen no confiable antes de interpolarlo
// en el log.
//
// El epilogo del log interpola nombres de fichero, URLs y mensajes de hallazgos
// que vienen del documento del usuario. Sin escapado, un documento que
// referencie una URL con marcado dentro conseguiria inyectarlo en el log.md
// publicado.
func sanitizeInline(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "`", "'")
	if len(s) > 300 {
		s = s[:297] + "..."
	}
	return strings.TrimSpace(s)
}

// StripTimeline elimina el bloque de cronologia. Lo usan los tests golden para
// comparar solo la parte determinista del bundle.
func StripTimeline(md []byte) []byte {
	s := string(md)
	i := strings.Index(s, TimelineOpen)
	j := strings.Index(s, TimelineClose)
	if i < 0 || j < 0 || j < i {
		return md
	}
	return []byte(s[:i] + s[j+len(TimelineClose):])
}
