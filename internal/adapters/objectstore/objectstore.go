// Package objectstore encapsula el almacenamiento de objetos (MinIO).
//
// Se usa minio-go y NO aws-sdk-go-v2, por dos razones concretas y verificables:
//
//  1. El firmante SigV4 de aws-sdk-go-v2 solo usa UNSIGNED-PAYLOAD sobre TLS.
//     Contra un endpoint http:// con un cuerpo no seekable y de longitud
//     desconocida -que es exactamente el caso de una subida multipart en
//     streaming- falla con "unseekable stream is not supported without TLS".
//     Eso tumbaria la pieza central del diseno: la API que no escribe a disco.
//  2. Desde 2025 ese SDK envia checksums CRC32 en codificacion aws-chunked por
//     defecto, que las releases de MinIO de 2024 no interpretan: el objeto se
//     almacena con las cabeceras de chunk incrustadas. Es un fallo SILENCIOSO,
//     el peor tipo posible.
//
// minio-go soporta PutObject con size=-1 de forma nativa y path-style
// automatico, sin ninguna de esas interacciones.
package objectstore

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/uniandes-isis4426/okfp/internal/config"
)

type Store struct {
	client    *minio.Client
	originals string
	bundles   string
}

func New(cfg config.S3Config) (*Store, error) {
	c, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure: cfg.UseSSL,
	})
	if err != nil {
		return nil, fmt.Errorf("cliente s3: %w", err)
	}
	return &Store{client: c, originals: cfg.BucketOriginals, bundles: cfg.BucketBundles}, nil
}

func (s *Store) BucketOriginals() string { return s.originals }
func (s *Store) BucketBundles() string   { return s.bundles }

// EnsureBuckets crea los buckets si no existen. Lo ejecuta el servicio one-shot
// de inicializacion, de modo que `docker compose up` deja el sistema operativo
// sin ningun paso manual.
func (s *Store) EnsureBuckets(ctx context.Context) error {
	for _, b := range []string{s.originals, s.bundles} {
		exists, err := s.client.BucketExists(ctx, b)
		if err != nil {
			return fmt.Errorf("comprobar bucket %s: %w", b, err)
		}
		if !exists {
			if err := s.client.MakeBucket(ctx, b, minio.MakeBucketOptions{}); err != nil {
				// Carrera benigna entre replicas.
				if exists, e2 := s.client.BucketExists(ctx, b); e2 == nil && exists {
					continue
				}
				return fmt.Errorf("crear bucket %s: %w", b, err)
			}
		}
	}
	return nil
}

func (s *Store) Ping(ctx context.Context) error {
	_, err := s.client.BucketExists(ctx, s.originals)
	return err
}

// --- Claves -----------------------------------------------------------------
//
// Todas las claves llevan el owner_id como primer segmento. Ademas de ordenar
// el bucket, hace que una fuga de claves no permita adivinar la de otro usuario
// y facilita inspeccionar el aislamiento en la consola de MinIO durante el
// video.

func OriginalKey(ownerID, documentID string) string {
	return fmt.Sprintf("%s/%s", ownerID, documentID)
}

// TempPrefix es donde se construye el bundle candidato mientras se valida.
// Incluye el intento para que dos intentos concurrentes del mismo trabajo no se
// pisen mientras compiten por el reclamo de publicacion.
func TempPrefix(jobID string, attempt int) string {
	return fmt.Sprintf("tmp/%s/%d/", jobID, attempt)
}

// PublishedPrefix es el prefijo servible. Es determinista a partir del trabajo:
// dos ejecuciones escriben el mismo sitio y no dos bundles distintos.
func PublishedPrefix(ownerID, jobID string) string {
	return fmt.Sprintf("bundles/%s/%s/", ownerID, jobID)
}

// QuarantinePrefix guarda los bundles que no superaron la validacion, para
// diagnostico. NINGUN endpoint sirve este prefijo: el usuario ve los hallazgos
// en el trabajo, pero no puede descargar el artefacto (condicion C4).
func QuarantinePrefix(ownerID, jobID string) string {
	return fmt.Sprintf("quarantine/%s/%s/", ownerID, jobID)
}

// --- Operaciones ------------------------------------------------------------

// PutStream sube un flujo de longitud desconocida (size=-1). Es lo que permite
// que la API copie el cuerpo multipart directamente al almacenamiento sin
// materializarlo en memoria ni en el disco del contenedor.
func (s *Store) PutStream(ctx context.Context, bucket, key string, r io.Reader, contentType string) (int64, error) {
	info, err := s.client.PutObject(ctx, bucket, key, r, -1, minio.PutObjectOptions{
		ContentType: contentType,
	})
	if err != nil {
		return 0, fmt.Errorf("subir %s/%s: %w", bucket, key, err)
	}
	return info.Size, nil
}

func (s *Store) PutBytes(ctx context.Context, bucket, key string, data []byte, contentType string) error {
	_, err := s.client.PutObject(ctx, bucket, key, strings.NewReader(string(data)), int64(len(data)),
		minio.PutObjectOptions{ContentType: contentType})
	if err != nil {
		return fmt.Errorf("subir %s/%s: %w", bucket, key, err)
	}
	return nil
}

func (s *Store) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	obj, err := s.client.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("obtener %s/%s: %w", bucket, key, err)
	}
	// GetObject es perezoso: el error real aparece en el primer Stat/Read.
	if _, err := obj.Stat(); err != nil {
		obj.Close()
		return nil, fmt.Errorf("obtener %s/%s: %w", bucket, key, err)
	}
	return obj, nil
}

func (s *Store) GetAll(ctx context.Context, bucket, key string, max int64) ([]byte, error) {
	rc, err := s.Get(ctx, bucket, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	return io.ReadAll(io.LimitReader(rc, max))
}

func (s *Store) Stat(ctx context.Context, bucket, key string) (minio.ObjectInfo, error) {
	return s.client.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
}

// Copy hace una copia servidor-a-servidor: los bytes no pasan por el worker.
func (s *Store) Copy(ctx context.Context, bucket, srcKey, dstKey string) error {
	_, err := s.client.CopyObject(ctx,
		minio.CopyDestOptions{Bucket: bucket, Object: dstKey},
		minio.CopySrcOptions{Bucket: bucket, Object: srcKey})
	if err != nil {
		return fmt.Errorf("copiar %s -> %s: %w", srcKey, dstKey, err)
	}
	return nil
}

func (s *Store) List(ctx context.Context, bucket, prefix string) ([]minio.ObjectInfo, error) {
	var out []minio.ObjectInfo
	for obj := range s.client.ListObjects(ctx, bucket, minio.ListObjectsOptions{
		Prefix: prefix, Recursive: true,
	}) {
		if obj.Err != nil {
			return nil, obj.Err
		}
		out = append(out, obj)
	}
	return out, nil
}

func (s *Store) RemovePrefix(ctx context.Context, bucket, prefix string) error {
	objects, err := s.List(ctx, bucket, prefix)
	if err != nil {
		return err
	}
	for _, o := range objects {
		if err := s.client.RemoveObject(ctx, bucket, o.Key, minio.RemoveObjectOptions{}); err != nil {
			return err
		}
	}
	return nil
}

// WaitReady bloquea hasta que el almacenamiento responde. Lo usan los servicios
// one-shot de arranque.
func (s *Store) WaitReady(ctx context.Context, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var last error
	for time.Now().Before(deadline) {
		if err := s.Ping(ctx); err == nil {
			return nil
		} else {
			last = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return fmt.Errorf("almacenamiento no disponible: %w", last)
}
