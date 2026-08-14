// Package parse convierte cada formato de entrada a la representacion
// intermedia comun.
package parse

import (
	"context"
	"fmt"
	"io"

	"github.com/uniandes-isis4426/okfp/internal/domain"
	"github.com/uniandes-isis4426/okfp/internal/okf/docmodel"
)

// Source es el documento de entrada.
//
// Expone io.ReaderAt y Size, y no un io.Reader desnudo, porque archive/zip
// exige acceso aleatorio para leer un DOCX. Con un lector secuencial cada
// parser tendria que bufferizar el fichero entero por su cuenta; asi el worker
// lo materializa una vez y todos comparten el mismo acceso.
type Source struct {
	R        io.ReaderAt
	Size     int64
	Name     string
	Format   domain.Format
	MimeType string
}

// Bytes lee el origen completo, respetando el limite configurado.
func (s *Source) Bytes(max int64) ([]byte, error) {
	n := s.Size
	if max > 0 && n > max {
		return nil, domain.PermanentFault("source_too_large",
			fmt.Sprintf("el documento supera el limite de parseo (%d bytes)", max), nil)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(io.NewSectionReader(s.R, 0, n), buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// Options traslada los limites de seguridad al parser.
//
// Van por parametro y no como constantes globales para poder probar cada cuota
// con valores minusculos, sin generar ficheros de 200 MB en los tests.
type Options struct {
	MaxBytes      int64
	MaxUnits      int
	AssetsMax     int64
	ZipMaxEntries int
	ZipMaxRatio   int
	// FetchRemote esta siempre desactivado. El worker vive en la red interna
	// junto a la base de datos, la cola y el almacenamiento: descargar una URL
	// controlada por el usuario lo convertiria en un proxy hacia esos
	// servicios. Validar el host no basta (redirecciones, DNS rebinding), y la
	// funcionalidad no aporta ningun punto de la rubrica.
	FetchRemote bool
}

// Parser es la interfaz que implementa cada formato.
type Parser interface {
	Format() domain.Format
	Parse(ctx context.Context, s *Source, o Options) (*docmodel.Document, error)
}

var registry = map[domain.Format]Parser{}

func register(p Parser) { registry[p.Format()] = p }

func init() {
	register(&MarkdownParser{})
	register(&TextParser{})
	register(&HTMLParser{})
	register(&DOCXParser{})
	register(&PDFParser{})
}

// For devuelve el parser del formato indicado.
func For(f domain.Format) (Parser, error) {
	p, ok := registry[f]
	if !ok {
		return nil, domain.ErrUnsupported.WithMessage(
			fmt.Sprintf("no hay conversor para el formato %q", f))
	}
	return p, nil
}
