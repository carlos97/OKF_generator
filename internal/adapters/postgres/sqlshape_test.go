package postgres

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// Estos tests inspeccionan el CODIGO FUENTE en lugar de ejecutar SQL.
//
// El motivo es concreto: un `RETURNING` que use columnas con alias de tabla es
// SQL sintacticamente correcto para el compilador de Go y solo falla al hablar
// con PostgreSQL, con `ERROR: missing FROM-clause entry for table "j"`. Un test
// unitario normal no lo ve, y el sintoma en produccion es un worker que no
// consigue reclamar ningun trabajo y deja la cola atascada sin que nada mas
// parezca roto. Revisarlo estaticamente cuesta milisegundos y cierra la clase
// entera de errores.

func readSource(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("no se pudo leer %s: %v", name, err)
	}
	return string(b)
}

// TestReturningNoUsaColumnasConAlias comprueba que ningun RETURNING interpola la
// lista de columnas con prefijo de alias.
func TestReturningNoUsaColumnasConAlias(t *testing.T) {
	for _, file := range []string{"jobs.go", "bundles.go", "documents.go", "users.go"} {
		src := readSource(t, file)

		// Busca `RETURNING `+jobCols` (la lista con alias) en cualquier variante
		// de espaciado.
		bad := regexp.MustCompile(`RETURNING\s*` + "`" + `\s*\+\s*jobCols\b`)
		if loc := bad.FindString(src); loc != "" {
			t.Errorf("%s: un RETURNING usa jobCols (columnas con prefijo j.); "+
				"debe usar jobColsBare, porque en UPDATE ... RETURNING no hay alias de tabla", file)
		}

		// Y al reves: la lista sin alias no tiene sentido en un SELECT con alias,
		// porque scanJob espera el mismo orden pero el SQL seria ambiguo si se
		// hace JOIN.
		badSelect := regexp.MustCompile(`SELECT\s*` + "`" + `\s*\+\s*jobColsBare\b`)
		if loc := badSelect.FindString(src); loc != "" {
			t.Errorf("%s: un SELECT con alias usa jobColsBare; use jobCols", file)
		}
	}
}

// TestListasDeColumnasCoinciden garantiza que las dos listas describen las
// mismas columnas y en el MISMO ORDEN.
//
// scanJob es comun a las consultas que usan una y otra lista, y lee los valores
// por posicion: si una lista anadiera una columna o cambiara el orden, el escaneo
// asignaria valores a los campos equivocados. Seria un fallo silencioso -datos
// cruzados, no un error- y por tanto el peor tipo posible.
func TestListasDeColumnasCoinciden(t *testing.T) {
	norm := func(s string) []string {
		var out []string
		for _, c := range strings.Split(s, ",") {
			c = strings.TrimSpace(c)
			c = strings.TrimPrefix(c, "j.")
			if c != "" {
				out = append(out, c)
			}
		}
		return out
	}

	conAlias := norm(jobCols)
	sinAlias := norm(jobColsBare)

	if len(conAlias) != len(sinAlias) {
		t.Fatalf("las listas tienen distinto numero de columnas: %d con alias, %d sin alias",
			len(conAlias), len(sinAlias))
	}
	for i := range conAlias {
		if conAlias[i] != sinAlias[i] {
			t.Errorf("posicion %d: jobCols tiene %q y jobColsBare tiene %q; "+
				"scanJob lee por posicion, asi que el orden debe ser identico",
				i, conAlias[i], sinAlias[i])
		}
	}

	// scanJob debe leer exactamente tantos campos como columnas hay.
	src := readSource(t, "jobs.go")
	start := strings.Index(src, "func scanJob(")
	if start < 0 {
		t.Fatal("no se encontro scanJob")
	}
	body := src[start:]
	if end := strings.Index(body, "\n}"); end > 0 {
		body = body[:end]
	}
	campos := strings.Count(body, "&j.") + strings.Count(body, "&leaseOwner") +
		strings.Count(body, "&resultCls") + strings.Count(body, "&okfGrade") +
		strings.Count(body, "&errCode") + strings.Count(body, "&errMsg") +
		strings.Count(body, "&report")
	if campos != len(conAlias) {
		t.Errorf("scanJob lee %d destinos pero la lista declara %d columnas",
			campos, len(conAlias))
	}
}
