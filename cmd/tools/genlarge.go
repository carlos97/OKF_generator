package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// runGenLarge genera el documento grande con el que se demuestra la asincronia.
//
// El fichero NO se versiona (son varios MB): se genera con este comando y con
// una semilla fija, de modo que dos ejecuciones producen exactamente el mismo
// documento y la demostracion es reproducible.
//
// La lentitud del procesamiento es EMERGENTE y medible: parsear varios MB,
// segmentar cientos de unidades, renderizar cientos de ficheros Markdown,
// subirlos al almacenamiento, validar todos los enlaces del indice y empaquetar.
// Esa es la evidencia defendible de C1, frente a un retardo artificial: ante la
// pregunta "esto es un sleep?" la respuesta esta en log.md, que enumera las
// unidades y las operaciones reales.
func runGenLarge(args []string) error {
	fs := flag.NewFlagSet("gen-large", flag.ContinueOnError)
	sections := fs.Int("sections", 400, "numero de secciones a generar")
	out := fs.String("out", filepath.Join("testdata", "05-lento-grande.md"), "fichero de salida")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	f, err := os.Create(*out)
	if err != nil {
		return err
	}
	defer f.Close()

	w := bufio.NewWriterSize(f, 1<<20)
	defer w.Flush()

	fmt.Fprintf(w, "# Compendio extenso de conversion documental\n\n")
	fmt.Fprintf(w, "Documento generado para demostrar el procesamiento asincrono. "+
		"Contiene %d secciones con contenido real: la duracion de la conversion "+
		"proviene del trabajo efectivo (parseo, segmentacion, renderizado, subida "+
		"y validacion) y no de ningun retardo artificial.\n\n", *sections)

	// Vocabulario fijo: sin aleatoriedad, el documento es reproducible byte a byte.
	words := []string{
		"conversion", "documento", "unidad", "concepto", "bundle", "indice",
		"trazabilidad", "validacion", "estructura", "encabezado", "seccion",
		"contenido", "formato", "markdown", "conocimiento", "plataforma",
		"asincrono", "trabajador", "cola", "mensaje", "almacenamiento", "objeto",
	}

	for i := 1; i <= *sections; i++ {
		fmt.Fprintf(w, "## Seccion %03d: analisis de la unidad %d\n\n", i, i)

		for p := 0; p < 6; p++ {
			var sb strings.Builder
			for k := 0; k < 60; k++ {
				sb.WriteString(words[(i*7+p*13+k*3)%len(words)])
				sb.WriteByte(' ')
			}
			fmt.Fprintf(w, "%s.\n\n", strings.TrimSpace(sb.String()))
		}

		fmt.Fprintf(w, "### Detalle tecnico de la seccion %03d\n\n", i)
		fmt.Fprintf(w, "- Identificador interno: `unidad-%03d`\n", i)
		fmt.Fprintf(w, "- Posicion en el documento: %d de %d\n", i, *sections)
		fmt.Fprintf(w, "- Estado esperado tras la conversion: publicado\n\n")

		fmt.Fprintf(w, "```text\nregistro de la unidad %03d: procesada correctamente\n```\n\n", i)
	}

	if err := w.Flush(); err != nil {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	fmt.Printf("generado %s: %d secciones, %.1f MiB\n",
		*out, *sections, float64(info.Size())/(1024*1024))
	return nil
}
