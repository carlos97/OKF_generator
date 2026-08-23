import { Component, input } from '@angular/core';

/**
 * Nombres de icono disponibles. Es una union de literales y no un string: si
 * una plantilla pide un icono que no existe, el error sale al compilar con
 * strictTemplates y no como un hueco vacio en la pantalla.
 */
export type IconName =
  | 'home'
  | 'upload'
  | 'stack'
  | 'search'
  | 'download'
  | 'file'
  | 'image'
  | 'logout'
  | 'back'
  | 'wave';

/**
 * Iconografia de la interfaz.
 *
 * Decisiones y su porque:
 *
 *   - Iconos LINEALES de 20-24 px con `stroke: currentColor`, tal como pide el
 *     documento de identidad (seccion 04). Al heredar el color, un icono dentro
 *     de un elemento activo cambia de tono sin una regla CSS por icono.
 *   - SVG en linea y NO una fuente de iconos ni una peticion a un CDN: la CSP
 *     del frontend es `default-src 'self'` sin excepciones para terceros, asi
 *     que cualquier icono remoto quedaria bloqueado por el navegador.
 *   - `aria-hidden` siempre: el icono acompana a un texto o a un
 *     `aria-label` del control que lo contiene. Un icono anunciado por el
 *     lector de pantalla junto a su propia etiqueta se leeria dos veces.
 *   - La forma tiene que ser reconocible sin color (seccion 05), asi que
 *     ninguna de estas siluetas depende del relleno para entenderse.
 */
@Component({
  selector: 'okf-icon',
  standalone: true,
  template: `
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      stroke-width="1.75"
      stroke-linecap="round"
      stroke-linejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      @switch (name()) {
        @case ('home') {
          <path d="M3 10.5 12 3l9 7.5" />
          <path d="M5.5 9.5V20h13V9.5" />
        }
        @case ('upload') {
          <path d="M12 16V4" />
          <path d="M7.5 8.5 12 4l4.5 4.5" />
          <path d="M4 15v3.5A1.5 1.5 0 0 0 5.5 20h13a1.5 1.5 0 0 0 1.5-1.5V15" />
        }
        @case ('stack') {
          <rect x="3.5" y="4" width="17" height="5" rx="1.5" />
          <rect x="3.5" y="11.5" width="17" height="5" rx="1.5" />
          <path d="M6.5 19.5h11" />
        }
        @case ('search') {
          <circle cx="11" cy="11" r="6.5" />
          <path d="m16 16 4 4" />
        }
        @case ('download') {
          <path d="M12 4v12" />
          <path d="M7.5 11.5 12 16l4.5-4.5" />
          <path d="M4 18.5h16" />
        }
        @case ('file') {
          <path d="M13.5 3.5H7a1.5 1.5 0 0 0-1.5 1.5v14A1.5 1.5 0 0 0 7 20.5h10a1.5 1.5 0 0 0 1.5-1.5V8.5z" />
          <path d="M13.5 3.5v5h5" />
        }
        @case ('image') {
          <rect x="3.5" y="4.5" width="17" height="15" rx="2" />
          <circle cx="9" cy="10" r="1.75" />
          <path d="m4.5 17.5 4.5-4 5 4.5 2.5-2.5 3 3" />
        }
        @case ('logout') {
          <path d="M15 8.5V6a1.5 1.5 0 0 0-1.5-1.5h-7A1.5 1.5 0 0 0 5 6v12a1.5 1.5 0 0 0 1.5 1.5h7A1.5 1.5 0 0 0 15 18v-2.5" />
          <path d="M10.5 12H20" />
          <path d="M17 9l3 3-3 3" />
        }
        @case ('back') {
          <path d="M14.5 6 8.5 12l6 6" />
        }
        @case ('wave') {
          <path d="M4 11v2" />
          <path d="M7.5 8.5v7" />
          <path d="M12 5.5v13" />
          <path d="M16.5 9.5v5" />
          <path d="M20 11v2" />
        }
      }
    </svg>
  `,
})
export class IconComponent {
  readonly name = input.required<IconName>();
}
