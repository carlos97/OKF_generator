// Package validate comprueba el bundle antes de publicarlo.
//
// Hay DOS ejes independientes y se calculan siempre los dos:
//
//   - VALIDEZ DE PLATAFORMA (codigos PLT-*): puerta binaria dura. Un solo
//     hallazgo ERROR impide la publicacion. Produce los tres veredictos que el
//     enunciado exige distinguir: valido, valido con advertencias, invalido.
//   - CONFORMIDAD OKF (codigos OKF-*): medida ponderada de calidad, 0-100, que
//     NUNCA bloquea. Se calcula tambien para los bundles invalidos.
//
// Mezclarlos en un unico numero haria imposible mostrar que un bundle puede ser
// perfectamente publicable y a la vez de baja calidad OKF, que es justo la
// distincion que se evalua.
package validate

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf/bundlefs"
	"github.com/uniandes-isis4426/okfp/internal/okf/render"
)

// Catalogo de reglas. Cada una tiene codigo estable, eje y severidad, para que
// el frontend pueda mostrarlas y el README documentarlas.
const (
	// --- Estructura minima (ERROR) ---
	PLTIndexMissing  = "PLT-STR-001" // falta index.md
	PLTLogMissing    = "PLT-STR-002" // falta log.md
	PLTNoConcepts    = "PLT-STR-003" // ningun documento de concepto
	PLTTOCMissing    = "PLT-STR-004" // falta el bloque de indice delimitado
	PLTOrphanConcept = "PLT-STR-005" // .md en la raiz no enlazado desde el indice

	// --- Enlaces (ERROR) ---
	PLTBrokenTOCLink    = "PLT-LNK-001" // enlace del indice que no resuelve
	PLTConceptNotInTOC  = "PLT-LNK-002" // concepto existente que el indice no enlaza
	PLTTOCOrderMismatch = "PLT-LNK-003" // el orden del indice no coincide con el de los conceptos

	// --- Front-matter (ERROR) ---
	PLTFrontMatterMissing = "PLT-FM-001" // falta el front-matter
	PLTFrontMatterBroken  = "PLT-FM-002" // front-matter sin delimitador de cierre

	// --- Seguridad y codificacion (ERROR) ---
	PLTUnsafePath = "PLT-SEC-001" // ruta absoluta o con ..
	PLTNotUTF8    = "PLT-ENC-001" // contenido que no es UTF-8 valido
	PLTEmptyFile  = "PLT-STR-006" // fichero de concepto vacio

	// --- Advertencias de plataforma (WARNING) ---
	PLTUserLinkUnresolved = "PLT-LNK-004" // enlace de la prosa del usuario que no resuelve
	PLTAssetMissing       = "PLT-AST-001" // referencia a un asset que no existe
	PLTLargeUnit          = "PLT-CNT-001" // unidad desproporcionadamente grande
)

// Options ajusta los umbrales de las advertencias.
type Options struct {
	LargeUnitBytes int
}

func defaultOptions() Options { return Options{LargeUnitBytes: 512 * 1024} }

var (
	relLinkRe  = regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
	imageRefRe = regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)
	orderFMRe  = regexp.MustCompile(`(?m)^order:\s*(\d+)\s*$`)
)

// Run evalua el bundle completo y devuelve el informe.
//
// Es una funcion pura sobre el sistema de ficheros en memoria: no toca red ni
// disco, de modo que puede ejecutarse dos veces (antes y despues de escribir el
// log definitivo) sin coste apreciable. Esa segunda pasada es la que garantiza
// que los bytes validados y los bytes publicados son los mismos.
func Run(fs *bundlefs.FS, opts *Options) *domain.ValidationReport {
	o := defaultOptions()
	if opts != nil {
		if opts.LargeUnitBytes > 0 {
			o.LargeUnitBytes = opts.LargeUnitBytes
		}
	}

	rep := &domain.ValidationReport{}
	add := func(code string, sev domain.Severity, path, msg string) {
		rep.Findings = append(rep.Findings, domain.Finding{
			Code: code, Axis: domain.AxisPlatform, Severity: sev, Message: msg, Path: path,
		})
	}
	rules := 0

	// --- Estructura minima --------------------------------------------------
	rules++
	indexData, hasIndex := fs.Get(bundlefs.IndexPath)
	if !hasIndex {
		add(PLTIndexMissing, domain.SeverityError, bundlefs.IndexPath,
			"El bundle no contiene index.md en la raiz")
	}

	rules++
	if !fs.Has(bundlefs.LogPath) {
		add(PLTLogMissing, domain.SeverityError, bundlefs.LogPath,
			"El bundle no contiene log.md en la raiz")
	}

	rules++
	concepts := fs.Concepts()
	if len(concepts) == 0 {
		add(PLTNoConcepts, domain.SeverityError, "",
			"El bundle no contiene ningun documento de concepto")
	}

	// --- Indice delimitado --------------------------------------------------
	rules++
	var tocLinks []string
	if hasIndex {
		var ok bool
		tocLinks, ok = extractTOC(string(indexData))
		if !ok {
			add(PLTTOCMissing, domain.SeverityError, bundlefs.IndexPath,
				"index.md no contiene un bloque de indice delimitado por "+render.TOCOpen)
		} else if len(tocLinks) == 0 {
			add(PLTTOCMissing, domain.SeverityError, bundlefs.IndexPath,
				"El bloque de indice de index.md no contiene ningun enlace")
		}
	}

	// --- Resolucion de los enlaces del indice -------------------------------
	rules++
	linked := map[string]bool{}
	for _, l := range tocLinks {
		target := cleanTarget(l)
		linked[target] = true
		if !fs.Has(target) {
			add(PLTBrokenTOCLink, domain.SeverityError, bundlefs.IndexPath,
				fmt.Sprintf("El indice enlaza %q, que no existe en el bundle", target))
		}
	}

	// --- Cobertura: todo concepto enlazado, ninguno huerfano ----------------
	rules++
	for _, c := range concepts {
		if !linked[c.Path] {
			add(PLTConceptNotInTOC, domain.SeverityError, c.Path,
				"El documento de concepto no esta enlazado desde el indice")
		}
	}

	rules++
	for _, c := range concepts {
		if !linked[c.Path] {
			add(PLTOrphanConcept, domain.SeverityError, c.Path,
				"Fichero Markdown en la raiz que no es index.md, log.md ni un concepto enlazado")
		}
	}

	// --- Orden ---------------------------------------------------------------
	//
	// El orden se compara contra el campo `order` del front-matter de cada
	// concepto, no contra el listado del almacenamiento (que es lexicografico y
	// romperia la secuencia en cuanto hubiera mas de nueve unidades).
	rules++
	var tocOrders []int
	orderOK := true
	for _, l := range tocLinks {
		data, ok := fs.Get(cleanTarget(l))
		if !ok {
			orderOK = false
			break
		}
		m := orderFMRe.FindSubmatch(data)
		if m == nil {
			orderOK = false
			break
		}
		var n int
		fmt.Sscanf(string(m[1]), "%d", &n)
		tocOrders = append(tocOrders, n)
	}
	if orderOK {
		for i := 1; i < len(tocOrders); i++ {
			if tocOrders[i] <= tocOrders[i-1] {
				add(PLTTOCOrderMismatch, domain.SeverityError, bundlefs.IndexPath,
					"Los enlaces del indice no siguen el orden del documento de origen")
				break
			}
		}
	}

	// --- Front-matter, codificacion, rutas y contenido ----------------------
	rules += 4
	for _, f := range fs.Files() {
		isMarkdown := strings.HasSuffix(f.Path, ".md")

		if !isPathSafe(f.Path) {
			add(PLTUnsafePath, domain.SeverityError, f.Path,
				"La ruta del fichero es absoluta o contiene segmentos de salida del bundle")
		}

		if !isMarkdown {
			continue
		}

		if !utf8.Valid(f.Data) {
			add(PLTNotUTF8, domain.SeverityError, f.Path,
				"El contenido no es UTF-8 valido")
		}

		if !strings.HasPrefix(string(f.Data), "---\n") {
			add(PLTFrontMatterMissing, domain.SeverityError, f.Path,
				"El fichero no comienza con un bloque de front-matter")
		} else if !strings.Contains(string(f.Data)[4:], "\n---") {
			add(PLTFrontMatterBroken, domain.SeverityError, f.Path,
				"El front-matter no tiene delimitador de cierre")
		}

		if f.Path != bundlefs.IndexPath && f.Path != bundlefs.LogPath {
			if len(strings.TrimSpace(stripFrontMatter(string(f.Data)))) == 0 {
				add(PLTEmptyFile, domain.SeverityError, f.Path,
					"El documento de concepto no tiene contenido")
			}
			if len(f.Data) > o.LargeUnitBytes {
				add(PLTLargeUnit, domain.SeverityWarning, f.Path,
					fmt.Sprintf("La unidad supera %d KiB; podria convenir dividirla", o.LargeUnitBytes/1024))
			}
		}
	}

	// --- Advertencias sobre enlaces de la prosa del usuario -----------------
	//
	// Estos enlaces son WARNING y no ERROR a proposito. El preambulo de
	// index.md y el cuerpo de los conceptos contienen texto del documento
	// original, que puede enlazar a ficheros que el usuario no subio. Tratarlo
	// como error invalidaria documentos legitimos: un README que empiece con
	// "ver [la guia](./CONTRIBUTING.md)" no puede considerarse un bundle
	// invalido.
	rules++
	for _, f := range fs.Files() {
		if !strings.HasSuffix(f.Path, ".md") {
			continue
		}
		body := string(f.Data)
		if f.Path == bundlefs.IndexPath {
			body = removeTOC(body)
		}
		for _, m := range relLinkRe.FindAllStringSubmatch(body, -1) {
			target := m[1]
			if isExternal(target) || strings.HasPrefix(target, "#") {
				continue
			}
			clean := cleanTarget(target)
			if clean == "" || fs.Has(clean) {
				continue
			}
			add(PLTUserLinkUnresolved, domain.SeverityWarning, f.Path,
				fmt.Sprintf("El enlace %q del contenido no resuelve dentro del bundle", target))
		}
	}

	// --- Assets referenciados ------------------------------------------------
	rules++
	for _, f := range fs.Files() {
		if !strings.HasSuffix(f.Path, ".md") {
			continue
		}
		for _, m := range imageRefRe.FindAllStringSubmatch(string(f.Data), -1) {
			target := m[1]
			if isExternal(target) {
				continue
			}
			clean := cleanTarget(target)
			if strings.HasPrefix(clean, bundlefs.AssetsDir) && !fs.Has(clean) {
				add(PLTAssetMissing, domain.SeverityWarning, f.Path,
					fmt.Sprintf("El recurso %q referenciado no existe en el bundle", target))
			}
		}
	}

	// --- Conformidad OKF (nunca bloquea) -------------------------------------
	conf := Conformance(fs)
	rep.Findings = append(rep.Findings, conf.Findings...)
	rep.OKFScore = conf.Score
	rep.OKFGrade = conf.Grade
	rules += conf.RulesEvaluated

	rep.RulesEvaluated = rules
	rep.Verdict = rep.Classify()
	return rep
}

// --- utilidades -------------------------------------------------------------

// extractTOC devuelve los destinos de los enlaces contenidos ENTRE los
// centinelas del indice. Solo estos enlaces se validan como obligatorios.
func extractTOC(index string) ([]string, bool) {
	i := strings.Index(index, render.TOCOpen)
	if i < 0 {
		return nil, false
	}
	j := strings.Index(index[i:], render.TOCClose)
	if j < 0 {
		return nil, false
	}
	block := index[i+len(render.TOCOpen) : i+j]

	var out []string
	for _, m := range relLinkRe.FindAllStringSubmatch(block, -1) {
		out = append(out, m[1])
	}
	return out, true
}

func removeTOC(index string) string {
	i := strings.Index(index, render.TOCOpen)
	if i < 0 {
		return index
	}
	j := strings.Index(index[i:], render.TOCClose)
	if j < 0 {
		return index
	}
	return index[:i] + index[i+j+len(render.TOCClose):]
}

func cleanTarget(t string) string {
	if i := strings.IndexByte(t, '#'); i >= 0 {
		t = t[:i]
	}
	t = strings.TrimSpace(t)
	t = strings.TrimPrefix(t, "./")
	return t
}

func isExternal(t string) bool {
	l := strings.ToLower(t)
	return strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") ||
		strings.HasPrefix(l, "mailto:") || strings.HasPrefix(l, "data:")
}

func isPathSafe(p string) bool {
	if strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	if p == ".." || strings.HasPrefix(p, "../") || strings.Contains(p, "/../") {
		return false
	}
	// Unidad de Windows tipo "C:"
	if len(p) > 1 && p[1] == ':' {
		return false
	}
	return true
}

func stripFrontMatter(s string) string {
	if !strings.HasPrefix(s, "---\n") {
		return s
	}
	rest := s[4:]
	if i := strings.Index(rest, "\n---"); i >= 0 {
		return rest[i+4:]
	}
	return rest
}
