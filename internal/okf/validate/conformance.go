package validate

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf/bundlefs"
	"github.com/uniandes-isis4426/okfp/internal/okf/render"
)

// Conformidad OKF: eje de CALIDAD, separado de la validez de plataforma.
//
// Ningun check de este fichero puede impedir la publicacion. Su unico producto
// es un score 0-100 y un grado, que se reportan aparte del veredicto. Esa
// separacion es deliberada: la validez responde "se publica?", la conformidad
// responde "que tan buen bundle OKF es?".
const (
	OKFFrontMatter   = "OKF-001" // front-matter canonico en todos los ficheros
	OKFSingleH1      = "OKF-002" // exactamente un H1 por concepto
	OKFExplicitOrder = "OKF-003" // orden declarado explicitamente
	OKFNavBlocks     = "OKF-004" // navegacion anterior/indice/siguiente
	OKFNoRawHTML     = "OKF-005" // sin HTML crudo en el Markdown
	OKFKebabNames    = "OKF-006" // nombres ASCII en kebab-case
	OKFLogStages     = "OKF-007" // log.md con las secciones minimas
	OKFAssetsUsed    = "OKF-008" // sin assets no referenciados
)

type check struct {
	code   string
	weight int
	desc   string
	eval   func(fs *bundlefs.FS) (bool, string)
}

// Los pesos suman 100 con los ocho checks implementados. Si se anadiera o
// quitara alguno habria que redistribuirlos, o el score maximo alcanzable
// dejaria de ser 100 y todos los bundles pareceria que fallan algo.
var checks = []check{
	{OKFFrontMatter, 15, "Front-matter canonico en index.md, log.md y conceptos", checkFrontMatter},
	{OKFSingleH1, 15, "Exactamente un encabezado de nivel 1 por concepto", checkSingleH1},
	{OKFExplicitOrder, 15, "Orden declarado explicitamente en cada concepto", checkExplicitOrder},
	{OKFNavBlocks, 15, "Bloques de navegacion anterior/indice/siguiente", checkNav},
	{OKFNoRawHTML, 10, "Sin HTML crudo en el Markdown publicado", checkNoRawHTML},
	{OKFKebabNames, 10, "Nombres de fichero ASCII en kebab-case", checkKebab},
	{OKFLogStages, 10, "log.md documenta unidades, transformaciones y validaciones", checkLogStages},
	{OKFAssetsUsed, 10, "Todos los recursos de assets/ estan referenciados", checkAssetsUsed},
}

type ConformanceResult struct {
	Score          int
	Grade          string
	Findings       []domain.Finding
	RulesEvaluated int
}

func Conformance(fs *bundlefs.FS) ConformanceResult {
	res := ConformanceResult{RulesEvaluated: len(checks)}
	total, earned := 0, 0

	for _, c := range checks {
		total += c.weight
		ok, detail := c.eval(fs)
		if ok {
			earned += c.weight
			continue
		}
		res.Findings = append(res.Findings, domain.Finding{
			Code:     c.code,
			Axis:     domain.AxisOKF,
			Severity: domain.SeverityInfo, // informativo: no bloquea ni degrada el veredicto
			Message:  fmt.Sprintf("%s: %s", c.desc, detail),
		})
	}

	if total > 0 {
		res.Score = earned * 100 / total
	}
	res.Grade = grade(res.Score)
	return res
}

func grade(score int) string {
	switch {
	case score >= 90:
		return "A"
	case score >= 75:
		return "B"
	case score >= 60:
		return "C"
	default:
		return "D"
	}
}

// --- checks -----------------------------------------------------------------

func checkFrontMatter(fs *bundlefs.FS) (bool, string) {
	for _, f := range fs.Files() {
		if !strings.HasSuffix(f.Path, ".md") {
			continue
		}
		s := string(f.Data)
		if !strings.HasPrefix(s, "---\n") || !strings.Contains(s, "okf_version:") {
			return false, "falta okf_version en " + f.Path
		}
	}
	return true, ""
}

var h1Re = regexp.MustCompile(`(?m)^#\s+\S`)

func checkSingleH1(fs *bundlefs.FS) (bool, string) {
	for _, c := range fs.Concepts() {
		n := len(h1Re.FindAllString(stripFrontMatter(string(c.Data)), -1))
		if n != 1 {
			return false, fmt.Sprintf("%s tiene %d encabezados de nivel 1", c.Path, n)
		}
	}
	return true, ""
}

func checkExplicitOrder(fs *bundlefs.FS) (bool, string) {
	for _, c := range fs.Concepts() {
		if !orderFMRe.Match(c.Data) {
			return false, c.Path + " no declara el campo order"
		}
	}
	return true, ""
}

func checkNav(fs *bundlefs.FS) (bool, string) {
	for _, c := range fs.Concepts() {
		if !strings.Contains(string(c.Data), render.NavOpen) {
			return false, c.Path + " no tiene bloque de navegacion"
		}
	}
	return true, ""
}

var rawHTMLRe = regexp.MustCompile(`(?i)<(script|iframe|object|embed|style|img|div|span)\b`)

func checkNoRawHTML(fs *bundlefs.FS) (bool, string) {
	for _, f := range fs.Files() {
		if !strings.HasSuffix(f.Path, ".md") {
			continue
		}
		if m := rawHTMLRe.FindString(string(f.Data)); m != "" {
			return false, fmt.Sprintf("%s contiene HTML crudo (%s)", f.Path, m)
		}
	}
	return true, ""
}

var kebabRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*\.[a-z0-9]+$`)

func checkKebab(fs *bundlefs.FS) (bool, string) {
	for _, c := range fs.Concepts() {
		if !kebabRe.MatchString(c.Path) {
			return false, c.Path + " no sigue el patron kebab-case ASCII"
		}
	}
	return true, ""
}

func checkLogStages(fs *bundlefs.FS) (bool, string) {
	data, ok := fs.Get(bundlefs.LogPath)
	if !ok {
		return false, "no hay log.md"
	}
	s := string(data)
	for _, section := range []string{"## Unidades detectadas", "## Transformaciones aplicadas", "## Validaciones", "## Veredicto"} {
		if !strings.Contains(s, section) {
			return false, "log.md no contiene la seccion " + section
		}
	}
	return true, ""
}

func checkAssetsUsed(fs *bundlefs.FS) (bool, string) {
	assets := fs.Assets()
	if len(assets) == 0 {
		return true, ""
	}
	referenced := map[string]bool{}
	for _, f := range fs.Files() {
		if !strings.HasSuffix(f.Path, ".md") {
			continue
		}
		for _, m := range imageRefRe.FindAllStringSubmatch(string(f.Data), -1) {
			referenced[cleanTarget(m[1])] = true
		}
	}
	for _, a := range assets {
		if !referenced[a.Path] {
			return false, a.Path + " no esta referenciado desde ningun concepto"
		}
	}
	return true, ""
}

// Rules expone el catalogo para documentarlo en el README y en el frontend.
func Rules() []map[string]any {
	out := make([]map[string]any, 0, len(checks))
	for _, c := range checks {
		out = append(out, map[string]any{"code": c.code, "weight": c.weight, "description": c.desc})
	}
	return out
}
