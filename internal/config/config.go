// Package config carga la configuracion desde variables de entorno.
//
// Las variables son DISCRETAS y no cadenas de conexion. Una contrasena "fuerte"
// con @ / : # ? dentro de una URL rompe el parseo sin percent-encoding, y el
// sintoma (host equivocado, porque todo lo posterior al @ se interpreta como
// host) aparece justo cuando el equipo hace lo que el README pide. Aqui la
// conexion se construye en Go, que se encarga del escapado.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	AppEnv   string
	LogLevel string
	HTTPAddr string

	DB    DBConfig
	AMQP  AMQPConfig
	S3    S3Config
	Auth  AuthConfig
	Limit LimitConfig
	Work  WorkerConfig

	DemoSlowModeMS int
	DevTools       bool

	// FaultInject permite demostrar en vivo la condicion literal del enunciado
	// ("ante la ausencia de index.md o log.md, la validacion falla"). Valores:
	// "" (desactivado), "drop_index", "drop_log".
	//
	// Existe porque un bundle generado correctamente nunca le falta un fichero
	// obligatorio: el caso hay que provocarlo. Para que siga siendo evidencia
	// honesta, esta desactivado por defecto, se registra en el log del worker y
	// deja un evento explicito en la traza del trabajo. No es la unica evidencia
	// de validacion: testdata/04-invalido.txt produce un bundle invalido por una
	// causa completamente natural.
	FaultInject string
}

type DBConfig struct {
	Host, User, Password, Name, SSLMode string
	Port                                int
	MaxConns                            int
}

type AMQPConfig struct {
	Host, User, Password, VHost string
	Port                        int
	Prefetch                    int
}

type S3Config struct {
	Endpoint        string
	AccessKey       string
	SecretKey       string
	UseSSL          bool
	BucketOriginals string
	BucketBundles   string
}

type AuthConfig struct {
	JWTSecret     string
	JWTTTL        time.Duration
	TicketTTL     time.Duration
	TicketMaxUses int
}

type LimitConfig struct {
	MaxUploadBytes int64
	ParseMaxBytes  int64
	MaxBundleBytes int64
	AssetsMaxBytes int64
	MaxUnits       int
	ZipMaxEntries  int
	ZipMaxRatio    int
}

type WorkerConfig struct {
	Concurrency      int
	JobTimeout       time.Duration
	JobLease         time.Duration
	SweeperInterval  time.Duration
	SweeperStaleAfte time.Duration
	MaxAttempts      int
	RetryDelay       time.Duration
	EnablePDF        bool
}

// Load lee el entorno. Todos los valores tienen default para que
// `docker compose up` funcione sin ningun .env; en APP_ENV=prod se exige
// explicitamente un JWT_SECRET propio.
func Load() (*Config, error) {
	c := &Config{
		AppEnv:   env("APP_ENV", "dev"),
		LogLevel: env("LOG_LEVEL", "info"),
		HTTPAddr: env("HTTP_ADDR", ":8080"),

		DB: DBConfig{
			Host:     env("POSTGRES_HOST", "postgres"),
			Port:     envInt("POSTGRES_PORT", 5432),
			User:     env("POSTGRES_USER", "postgres"),
			Password: env("POSTGRES_PASSWORD", "admin"),
			Name:     env("POSTGRES_DB", "okf"),
			SSLMode:  env("POSTGRES_SSLMODE", "disable"),
			MaxConns: envInt("POSTGRES_MAX_CONNS", 10),
		},
		AMQP: AMQPConfig{
			Host:     env("RABBITMQ_HOST", "rabbitmq"),
			Port:     envInt("RABBITMQ_PORT", 5672),
			User:     env("RABBITMQ_USER", "okf"),
			Password: env("RABBITMQ_PASSWORD", "okf_dev_password"),
			VHost:    env("RABBITMQ_VHOST", "/"),
			Prefetch: envInt("RABBITMQ_PREFETCH", 1),
		},
		S3: S3Config{
			Endpoint:        env("S3_ENDPOINT", "minio:9000"),
			AccessKey:       env("S3_ACCESS_KEY", "okfadmin"),
			SecretKey:       env("S3_SECRET_KEY", "okfadmin_dev_password"),
			UseSSL:          envBool("S3_USE_SSL", false),
			BucketOriginals: env("S3_BUCKET_ORIGINALS", "okf-originals"),
			BucketBundles:   env("S3_BUCKET_BUNDLES", "okf-bundles"),
		},
		Auth: AuthConfig{
			JWTSecret:     env("JWT_SECRET", "dev-secret-cambiar-en-produccion-min-32-bytes"),
			JWTTTL:        envDur("JWT_TTL", 24*time.Hour),
			TicketTTL:     envDur("DOWNLOAD_TICKET_TTL", 120*time.Second),
			TicketMaxUses: envInt("DOWNLOAD_TICKET_MAX_USES", 3),
		},
		Limit: LimitConfig{
			MaxUploadBytes: envInt64("MAX_UPLOAD_BYTES", 20*1024*1024),
			ParseMaxBytes:  envInt64("PARSE_MAX_BYTES", 5*1024*1024),
			MaxBundleBytes: envInt64("MAX_BUNDLE_BYTES", 64*1024*1024),
			AssetsMaxBytes: envInt64("ASSETS_MAX_BYTES", 16*1024*1024),
			MaxUnits:       envInt("MAX_UNITS", 500),
			ZipMaxEntries:  envInt("ZIP_MAX_ENTRIES", 2000),
			ZipMaxRatio:    envInt("ZIP_MAX_RATIO", 100),
		},
		Work: WorkerConfig{
			Concurrency:      envInt("WORKER_CONCURRENCY", 1),
			JobTimeout:       envDur("JOB_TIMEOUT", 120*time.Second),
			JobLease:         envDur("JOB_LEASE", 180*time.Second),
			SweeperInterval:  envDur("SWEEPER_INTERVAL", 30*time.Second),
			SweeperStaleAfte: envDur("SWEEPER_STALE_AFTER", 60*time.Second),
			MaxAttempts:      envInt("MAX_ATTEMPTS", 3),
			RetryDelay:       envDur("RETRY_DELAY", 30*time.Second),
			EnablePDF:        envBool("ENABLE_PDF", false),
		},

		DemoSlowModeMS: envInt("DEMO_SLOW_MODE_MS", 0),
		DevTools:       envBool("DEV_TOOLS", false),
		FaultInject:    env("OKF_FAULT_INJECT", ""),
	}

	return c, c.validate()
}

func (c *Config) validate() error {
	var problems []string

	// El lease debe sobrevivir al trabajo mas largo posible; si no, un trabajo
	// legitimo seria robado por otro worker a mitad y se producirian dos
	// conversiones concurrentes del mismo documento.
	if c.Work.JobLease <= c.Work.JobTimeout {
		problems = append(problems, fmt.Sprintf(
			"JOB_LEASE (%s) debe ser mayor que JOB_TIMEOUT (%s) mas un margen de subida",
			c.Work.JobLease, c.Work.JobTimeout))
	}
	if c.Work.Concurrency < 1 {
		problems = append(problems, "WORKER_CONCURRENCY debe ser al menos 1")
	}
	if c.Work.MaxAttempts < 1 {
		problems = append(problems, "MAX_ATTEMPTS debe ser al menos 1")
	}
	if c.Work.RetryDelay <= 0 {
		problems = append(problems, "RETRY_DELAY debe ser mayor que cero")
	}
	if c.Limit.ParseMaxBytes > c.Limit.MaxUploadBytes {
		problems = append(problems, "PARSE_MAX_BYTES no puede superar MAX_UPLOAD_BYTES")
	}
	if c.AppEnv == "prod" {
		if strings.HasPrefix(c.Auth.JWTSecret, "dev-secret") || len(c.Auth.JWTSecret) < 32 {
			problems = append(problems, "en APP_ENV=prod hay que definir un JWT_SECRET propio de al menos 32 bytes")
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("configuracion invalida:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

// DSN construye la cadena de conexion de PostgreSQL en formato clave=valor,
// que tolera caracteres especiales sin percent-encoding (a diferencia de la
// forma URL).
//
// Contiene UNICAMENTE parametros que entiende el servidor. Los ajustes del pool
// (numero maximo de conexiones, etc.) NO van aqui: `pool_max_conns` es una
// extension de pgxpool y una conexion pgx simple -como la que usa el migrador-
// reenvia al servidor cualquier clave que no reconoce como parametro de
// arranque, con lo que PostgreSQL aborta con
// `FATAL: unrecognized configuration parameter "pool_max_conns"`.
// El tamano del pool se aplica de forma programatica en postgres.Connect.
func (d DBConfig) DSN() string {
	esc := func(s string) string {
		s = strings.ReplaceAll(s, `\`, `\\`)
		s = strings.ReplaceAll(s, `'`, `\'`)
		return s
	}
	return fmt.Sprintf(
		"host='%s' port=%d user='%s' password='%s' dbname='%s' sslmode=%s",
		esc(d.Host), d.Port, esc(d.User), esc(d.Password), esc(d.Name), d.SSLMode)
}

// URL construye la URI de AMQP con percent-encoding correcto de credenciales.
func (a AMQPConfig) URL() string {
	u := url.URL{
		Scheme: "amqp",
		User:   url.UserPassword(a.User, a.Password),
		Host:   fmt.Sprintf("%s:%d", a.Host, a.Port),
		Path:   a.VHost,
	}
	return u.String()
}

// --- helpers ----------------------------------------------------------------

func env(k, def string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func envInt64(k string, def int64) int64 {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
			return n
		}
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	}
	return def
}

func envDur(k string, def time.Duration) time.Duration {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		if d, err := time.ParseDuration(strings.TrimSpace(v)); err == nil {
			return d
		}
	}
	return def
}
