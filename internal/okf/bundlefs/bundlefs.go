// Package bundlefs es el sistema de ficheros en memoria del bundle candidato.
//
// El bundle se construye y se VALIDA aqui, antes de escribir un solo byte en el
// almacenamiento. Es lo que permite que un bundle invalido nunca llegue al
// prefijo servible: la condicion "el bundle no se publica y no se habilita su
// descarga" queda garantizada por el orden de las operaciones y no por un `if`
// en el handler de descarga.
package bundlefs

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

const (
	IndexPath = "index.md"
	LogPath   = "log.md"
	AssetsDir = "assets/"
)

type File struct {
	Path string
	Data []byte
	// Seq define el ORDEN canonico del bundle. Nunca se deriva del listado del
	// almacenamiento, que devuelve las claves en orden lexicografico y
	// destrozaria la secuencia de conceptos en cuanto hubiera mas de nueve.
	Seq int
}

type FS struct {
	files map[string]*File
	next  int
}

func New() *FS { return &FS{files: make(map[string]*File)} }

// Put anade o reemplaza un fichero conservando su posicion si ya existia.
func (f *FS) Put(path string, data []byte) {
	if old, ok := f.files[path]; ok {
		old.Data = data
		return
	}
	f.files[path] = &File{Path: path, Data: data, Seq: f.next}
	f.next++
}

func (f *FS) Get(path string) ([]byte, bool) {
	file, ok := f.files[path]
	if !ok {
		return nil, false
	}
	return file.Data, true
}

func (f *FS) Has(path string) bool {
	_, ok := f.files[path]
	return ok
}

func (f *FS) Delete(path string) { delete(f.files, path) }

func (f *FS) Len() int { return len(f.files) }

// TotalBytes es el tamano del bundle completo, usado para aplicar el limite
// configurado y para el manifiesto.
func (f *FS) TotalBytes() int64 {
	var n int64
	for _, file := range f.files {
		n += int64(len(file.Data))
	}
	return n
}

// Files devuelve los ficheros en ORDEN CANONICO: index.md, log.md, los
// conceptos por su secuencia y finalmente los assets en orden lexicografico.
//
// Un orden fijo hace que dos empaquetados del mismo bundle produzcan el mismo
// ZIP, lo que permite comparar hashes en la sustentacion como evidencia de que
// no hay estado ni aleatoriedad en la API.
func (f *FS) Files() []File {
	out := make([]File, 0, len(f.files))
	for _, file := range f.files {
		out = append(out, *file)
	}
	sort.Slice(out, func(i, j int) bool {
		return rank(out[i]) < rank(out[j])
	})
	return out
}

func rank(f File) string {
	switch {
	case f.Path == IndexPath:
		return "0"
	case f.Path == LogPath:
		return "1"
	case strings.HasPrefix(f.Path, AssetsDir):
		return "3:" + f.Path
	default:
		return fmt.Sprintf("2:%06d:%s", f.Seq, f.Path)
	}
}

// Concepts devuelve los documentos de concepto (todo .md de la raiz que no sea
// index.md ni log.md), en orden.
func (f *FS) Concepts() []File {
	var out []File
	for _, file := range f.Files() {
		if file.Path == IndexPath || file.Path == LogPath {
			continue
		}
		if strings.Contains(file.Path, "/") {
			continue
		}
		if strings.HasSuffix(file.Path, ".md") {
			out = append(out, file)
		}
	}
	return out
}

// Assets devuelve los recursos extraidos.
func (f *FS) Assets() []File {
	var out []File
	for _, file := range f.Files() {
		if strings.HasPrefix(file.Path, AssetsDir) {
			out = append(out, file)
		}
	}
	return out
}

// SHA256 del contenido de un fichero, para el manifiesto.
func SHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
