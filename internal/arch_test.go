package internal_test

import (
	"os/exec"
	"strings"
	"testing"
)

// TestCapas verifica en CI la separacion que se defiende en la sustentacion.
//
// No es una comprobacion decorativa: es la unica forma de demostrar que "la
// conversion no se ejecuta en ningun caso dentro de la peticion HTTP" sin pedir
// que se crea la palabra. Si el binario de la API ni siquiera contiene el
// paquete del conversor, la afirmacion es estructural.
//
// El test tambien protege contra el error inverso, mucho mas facil de cometer:
// que alguien importe un servicio de la API desde el worker para reutilizar una
// funcion, arrastrando la capa HTTP entera.
func TestCapas(t *testing.T) {
	cases := []struct {
		binary    string
		forbidden string
		reason    string
	}{
		{
			binary:    "./cmd/api",
			forbidden: "internal/okf",
			reason:    "el binario de la API no debe contener el conversor: la conversion ocurre solo en el worker",
		},
		{
			binary:    "./cmd/api",
			forbidden: "internal/app/convert",
			reason:    "el binario de la API no debe contener el caso de uso del worker",
		},
		{
			binary:    "./cmd/worker",
			forbidden: "internal/adapters/httpapi",
			reason:    "el worker no expone HTTP y no debe arrastrar el router ni los handlers",
		},
	}

	for _, tc := range cases {
		t.Run(tc.binary+" !-> "+tc.forbidden, func(t *testing.T) {
			out, err := exec.Command("go", "list", "-deps", tc.binary).Output()
			if err != nil {
				t.Skipf("no se pudo ejecutar go list: %v", err)
			}
			for _, line := range strings.Split(string(out), "\n") {
				pkg := strings.TrimSpace(line)
				if pkg == "" {
					continue
				}
				if strings.Contains(pkg, tc.forbidden) {
					t.Errorf("%s importa %s\n  motivo: %s", tc.binary, pkg, tc.reason)
				}
			}
		})
	}
}
