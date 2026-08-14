import { Pipe, PipeTransform, inject } from '@angular/core';
import { DomSanitizer, SafeHtml } from '@angular/platform-browser';
import DOMPurify from 'dompurify';
import MarkdownIt from 'markdown-it';

/**
 * Renderiza Markdown de forma segura.
 *
 * Defensa en profundidad, en tres capas, porque el contenido proviene de un
 * documento subido por un usuario y por tanto es NO CONFIABLE:
 *
 *  1. `html: false` en markdown-it: el HTML embebido en el Markdown ni se
 *     interpreta, se escapa como texto. Un `<script>` dentro de un concepto
 *     aparece literalmente en pantalla y nunca llega a existir como elemento.
 *     (marked no ofrece este interruptor, y es la razon de elegir markdown-it.)
 *  2. DOMPurify sobre el HTML resultante, con una lista blanca de etiquetas.
 *  3. `bypassSecurityTrustHtml` se aplica UNICAMENTE despues de las dos
 *     anteriores, nunca sobre la entrada cruda.
 */
@Pipe({ name: 'markdown', standalone: true })
export class MarkdownPipe implements PipeTransform {
  private readonly sanitizer = inject(DomSanitizer);

  private readonly md = new MarkdownIt({
    html: false,
    linkify: false,
    breaks: false,
    typographer: false,
  });

  transform(value: string | null | undefined): SafeHtml {
    if (!value) {
      return '';
    }

    // El front-matter se muestra aparte: incluirlo en el render lo convertiria
    // en un encabezado fantasma (el --- de cierre produce un setext H2).
    const body = stripFrontMatter(value);

    const rendered = this.md.render(body);
    const clean = DOMPurify.sanitize(rendered, {
      ALLOWED_TAGS: [
        'h1', 'h2', 'h3', 'h4', 'h5', 'h6',
        'p', 'br', 'hr', 'strong', 'em', 'del', 'code', 'pre',
        'ul', 'ol', 'li', 'blockquote',
        'table', 'thead', 'tbody', 'tr', 'th', 'td',
        'a', 'img',
      ],
      ALLOWED_ATTR: ['href', 'src', 'alt', 'title', 'class'],
      // Sin data: en los enlaces; las imagenes del bundle se sirven por la API.
      ALLOWED_URI_REGEXP: /^(?:https?:|mailto:|[^a-z]|[a-z+.-]+(?:[^a-z+.\-:]|$))/i,
      FORBID_TAGS: ['style', 'script', 'iframe', 'object', 'embed', 'form'],
      FORBID_ATTR: ['style', 'onerror', 'onload', 'onclick'],
    });

    return this.sanitizer.bypassSecurityTrustHtml(clean);
  }
}

/** Separa el bloque YAML inicial del cuerpo Markdown. */
export function stripFrontMatter(text: string): string {
  if (!text.startsWith('---')) {
    return text;
  }
  const end = text.indexOf('\n---', 3);
  if (end < 0) {
    return text;
  }
  const after = text.indexOf('\n', end + 1);
  return after < 0 ? '' : text.slice(after + 1);
}

/** Extrae el front-matter como pares clave/valor, para mostrarlo como metadatos. */
export function parseFrontMatter(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  if (!text.startsWith('---')) {
    return out;
  }
  const end = text.indexOf('\n---', 3);
  if (end < 0) {
    return out;
  }
  for (const line of text.slice(4, end).split('\n')) {
    const i = line.indexOf(':');
    if (i > 0) {
      const key = line.slice(0, i).trim();
      const value = line.slice(i + 1).trim().replace(/^"|"$/g, '');
      out[key] = value;
    }
  }
  return out;
}
