package postgres

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestNingunaConsultaSinPropietario es un test de código, no de comportamiento.
//
// Analiza el árbol sintáctico de este paquete y comprueba que toda consulta SQL
// sobre las tablas que contienen recursos de usuario lleva un filtro por
// propietario, salvo las funciones marcadas explícitamente como internas del
// worker.
//
// Existe porque el fallo de aislamiento no llega escribiendo mal una consulta,
// sino AÑADIENDO un endpoint nuevo seis días después y olvidando el filtro. Un
// test de comportamiento sólo cubre las rutas que alguien se acordó de probar;
// éste cubre las que aún no existen.
//
// El diseño lo hace comprobable: `ownerID` va en la FIRMA de cada función del
// repositorio, así que una consulta sin propietario es visible en el código.
func TestNingunaConsultaSinPropietario(t *testing.T) {
	// Funciones que legítimamente consultan sin propietario, con su motivo.
	// Añadir una entrada aquí es una decisión consciente y revisable.
	permitidas := map[string]string{
		"GetInternal":          "la usa el worker, que llega desde el mensaje de la cola y resuelve la propiedad a través del trabajo",
		"ByEmail":              "el login necesita buscar por correo antes de que exista sesión",
		"ByID":                 "resolución de usuario a partir del token ya validado",
		"Create":               "inserción de usuario",
		"CreateWithDocument":   "inserción: el propietario se escribe, no se filtra",
		"CreateRetry":          "resuelve el padre con Get(ownerID, ...) antes de insertar",
		"MarkEnqueued":         "transición interna por identificador de trabajo, sin exponer datos",
		"Claim":                "transición del worker sobre un trabajo tomado de la cola",
		"RenewLease":           "renovación del arrendamiento del worker",
		"IsCancelRequested":    "consulta de una bandera booleana, no expone contenido",
		"FinishCanceled":       "transición del worker",
		"MarkInvalid":          "transición del worker, acotada por lease_owner",
		"ScheduleRetry":        "transición del worker, acotada por lease_owner",
		"PendingOutbox":        "barredor interno; reconstruye mensajes de trabajos sin exponerlos",
		"MarkOutboxPublished":  "barredor interno tras confirmación de RabbitMQ",
		"MarkFailed":           "transición del worker, acotada por lease_owner",
		"StaleQueued":          "barrido interno del sistema",
		"ReclaimExpiredLeases": "barrido interno del sistema",
		"CancelExpiredLeases":  "barrido interno que finaliza cancelaciones abandonadas, sin exponer datos",
		"TryAdvisoryLock":      "cerrojo de aviso, no toca datos de usuario",
		"AdvisoryUnlock":       "cerrojo de aviso, no toca datos de usuario",
		"AppendEvent":          "inserción de traza para un trabajo ya resuelto",
		"Files":                "se llama siempre desde GetPublished, que ya filtró por propietario",
		"ClaimForPublish":      "publicación del worker, acotada por lease_owner",
		"Publish":              "publicación del worker, acotada por lease_owner",
		"DeleteClaim":          "deshace un reclamo propio del worker",
		"RedeemTicket":         "el ticket lleva el propietario y se compara después",
		"PurgeExpiredTickets":  "limpieza de tickets caducados",
	}

	// Tablas cuyas filas pertenecen a un usuario concreto.
	tablasProtegidas := []string{"documents", "jobs", "bundles", "bundle_files", "job_events"}

	fset := token.NewFileSet()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	revisadas := 0

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if _, ok := permitidas[fn.Name.Name]; ok {
				continue
			}

			// Recoger las cadenas literales de la función: ahí viven las consultas.
			var consultas []string
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
					consultas = append(consultas, lit.Value)
				}
				return true
			})

			sql := strings.ToLower(strings.Join(consultas, " "))
			if !strings.Contains(sql, "select") && !strings.Contains(sql, "update") &&
				!strings.Contains(sql, "delete") {
				continue
			}

			tocaTablaProtegida := false
			for _, tabla := range tablasProtegidas {
				if strings.Contains(sql, " "+tabla+" ") || strings.Contains(sql, " "+tabla+"\n") {
					tocaTablaProtegida = true
					break
				}
			}
			if !tocaTablaProtegida {
				continue
			}

			revisadas++

			if !strings.Contains(sql, "owner_id") {
				t.Errorf("%s: la función %s consulta una tabla de recursos de usuario sin filtrar por owner_id.\n"+
					"  Si es intencional, añádala al mapa `permitidas` de este test con su motivo,\n"+
					"  para que la excepción quede documentada y revisada.",
					filepath.Base(path), fn.Name.Name)
			}
		}
	}

	if revisadas == 0 {
		t.Fatal("el test no encontró ninguna consulta que revisar: probablemente el análisis está roto")
	}
	t.Logf("%d función(es) con consultas sobre recursos de usuario revisadas, todas filtran por propietario", revisadas)
}
