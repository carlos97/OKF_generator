package render

import (
	"fmt"
	"strings"

	"github.com/uniandes-isis4426/okfp/internal/okf/segment"
)

// Index renderiza index.md: la navegacion y los datos del bundle.
//
// El bloque de conceptos va DELIMITADO por centinelas. Fuera de ellos el
// fichero puede contener lo que haga falta (la descripcion, el enlace a
// log.md), y eso no interfiere con la validacion de enlaces.
func Index(meta Meta, units []segment.Unit, description string) []byte {
	var b strings.Builder

	b.WriteString("---\n")
	b.WriteString(fmt.Sprintf("okf_version: %q\n", OKFVersion))
	b.WriteString("type: \"bundle\"\n")
	b.WriteString(fmt.Sprintf("bundle_id: %q\n", meta.BundleID))
	b.WriteString(fmt.Sprintf("job_id: %q\n", meta.JobID))
	b.WriteString(fmt.Sprintf("title: %q\n", meta.Title))
	b.WriteString(fmt.Sprintf("source: %q\n", meta.SourceName))
	b.WriteString(fmt.Sprintf("source_format: %q\n", meta.SourceFmt))
	b.WriteString(fmt.Sprintf("units: %d\n", meta.UnitCount))
	b.WriteString(fmt.Sprintf("generated_at: %q\n", meta.GeneratedAt))
	b.WriteString("---\n\n")

	b.WriteString("# " + escapeInline(meta.Title) + "\n\n")

	if strings.TrimSpace(description) != "" {
		b.WriteString("## Descripcion\n\n")
		b.WriteString(strings.TrimSpace(description) + "\n\n")
	}

	b.WriteString("## Indice de conceptos\n\n")
	b.WriteString(TOCOpen + "\n")
	for _, u := range units {
		b.WriteString(fmt.Sprintf("%d. [%s](./%s)\n", u.Order, escapeInline(u.Title), u.Filename))
	}
	b.WriteString(TOCClose + "\n\n")

	b.WriteString("## Contenido del bundle\n\n")
	b.WriteString("| Fichero | Descripcion |\n| --- | --- |\n")
	b.WriteString("| `index.md` | Este indice |\n")
	b.WriteString("| `log.md` | Trazabilidad completa de la conversion |\n")
	b.WriteString(fmt.Sprintf("| %d documento(s) de concepto | Unidades logicas detectadas en el documento de origen |\n",
		len(units)))

	return []byte(b.String())
}
