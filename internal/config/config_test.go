package config_test

import (
	"strings"
	"testing"

	"github.com/uniandes-isis4426/okfp/internal/config"
)

// TestDSNNoLlevaParametrosDePool fija una regresion real.
//
// `pool_max_conns` es una extension de pgxpool, no un parametro de PostgreSQL.
// Si viaja en el DSN, pgxpool lo consume sin problema, pero una conexion pgx
// simple -la que usa el migrador- reenvia al servidor toda clave que no
// reconoce como parametro de arranque, y PostgreSQL aborta la conexion con:
//
//	FATAL: unrecognized configuration parameter "pool_max_conns"
//
// El sintoma aparece solo al arrancar el servicio `migrator` con un servidor
// real, es decir, nunca en los tests unitarios ni en la compilacion. De ahi este
// test.
func TestDSNNoLlevaParametrosDePool(t *testing.T) {
	t.Setenv("POSTGRES_MAX_CONNS", "25")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	dsn := cfg.DB.DSN()

	for _, prohibido := range []string{
		"pool_max_conns",
		"pool_min_conns",
		"pool_max_conn_lifetime",
		"pool_max_conn_idle_time",
		"pool_health_check_period",
	} {
		if strings.Contains(dsn, prohibido) {
			t.Errorf("el DSN contiene %q, que PostgreSQL rechaza: %s", prohibido, dsn)
		}
	}

	// El valor sigue disponible para aplicarlo de forma programatica al pool.
	if cfg.DB.MaxConns != 25 {
		t.Errorf("MaxConns = %d, se esperaba 25", cfg.DB.MaxConns)
	}
}

// TestDSNToleraCaracteresEspeciales justifica el formato clave=valor frente a la
// forma URL.
//
// El README pide generar una contrasena fuerte. Con una URL, cualquier `@` haria
// que todo lo posterior se interpretara como host y el servicio arrancaria
// apuntando a un sitio equivocado (o fallaria con un mensaje que no senala la
// causa). El formato clave=valor con comillas no tiene ese problema.
func TestDSNToleraCaracteresEspeciales(t *testing.T) {
	const clave = `p@ss:w/rd#con?simbolos'y\barras`
	t.Setenv("POSTGRES_PASSWORD", clave)
	t.Setenv("POSTGRES_HOST", "postgres")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	dsn := cfg.DB.DSN()

	// El host debe seguir siendo el correcto: es lo que se rompe con una URL.
	if !strings.Contains(dsn, "host='postgres'") {
		t.Errorf("el host no se preservo: %s", dsn)
	}
	// Las comillas simples de la contrasena deben ir escapadas.
	if !strings.Contains(dsn, `\'`) {
		t.Errorf("la comilla simple de la contrasena no se escapo: %s", dsn)
	}
}

// TestLeaseMayorQueTimeout comprueba la validacion de coherencia temporal.
//
// Si el lease no sobrevive al trabajo mas largo posible, otro worker robaria un
// trabajo legitimo a mitad y se produrian dos conversiones concurrentes del
// mismo documento. Arrancar con esa configuracion debe fallar de inmediato y con
// un mensaje claro, no degradarse en una carrera intermitente.
func TestLeaseMayorQueTimeout(t *testing.T) {
	t.Setenv("JOB_TIMEOUT", "120s")
	t.Setenv("JOB_LEASE", "60s")

	if _, err := config.Load(); err == nil {
		t.Fatal("se esperaba un error de configuracion con JOB_LEASE < JOB_TIMEOUT")
	} else if !strings.Contains(err.Error(), "JOB_LEASE") {
		t.Errorf("el mensaje de error no menciona JOB_LEASE: %v", err)
	}
}
