import { DecimalPipe } from '@angular/common';
import { Component, OnDestroy, OnInit, computed, inject, input, signal } from '@angular/core';
import { RouterLink } from '@angular/router';

import { ApiService } from '../../core/api.service';
import { Bundle, BundleFile } from '../../core/models';
import { IconComponent } from '../../shared/icon.component';
import { MarkdownPipe, parseFrontMatter } from '../../shared/markdown.pipe';
import { describeError } from '../auth/login.component';

@Component({
  selector: 'okf-bundle-viewer',
  standalone: true,
  imports: [RouterLink, DecimalPipe, IconComponent, MarkdownPipe],
  template: `
    <a routerLink="/" class="back">
      <okf-icon name="back" />
      Volver al panel
    </a>

    @if (error()) {
      <div class="alert alert--err top">{{ error() }}</div>
    }

    @if (bundle(); as b) {
      <section class="card top enter">
        <div class="spread">
          <div class="ident">
            <p class="eyebrow">Bundle</p>
            <h1 class="title">Bundle OKF</h1>
            <code class="small">{{ b.id }}</code>
            @if (b.source_filename) {
              <div class="meta source">
                generado desde <strong>{{ b.source_filename }}</strong>
              </div>
            }
          </div>
          <div class="row">
            <span class="badge badge--ok">Publicado</span>
            <button type="button" class="btn-secondary" (click)="downloadOriginal()">
              <okf-icon name="file" />
              Original
            </button>
            <button type="button" class="btn-primary" (click)="download()">
              <okf-icon name="download" />
              Descargar ZIP
            </button>
          </div>
        </div>

        <div class="tiles">
          <div class="tile">
            <span class="tile-label">Unidades</span>
            <span class="tile-value">{{ b.unit_count }}</span>
          </div>
          <div class="tile">
            <span class="tile-label">Tamano</span>
            <span class="tile-value">{{ (b.total_bytes / 1024) | number: '1.0-1' }} KiB</span>
          </div>
          <div class="tile">
            <span class="tile-label">Ficheros</span>
            <span class="tile-value">{{ (b.files ?? []).length }}</span>
          </div>
        </div>
      </section>

      <div class="viewer">
        <!-- --------------------------------------------- arbol de ficheros -- -->
        <aside class="card tree">
          <p class="eyebrow">Contenido</p>
          <ul>
            @for (f of rootFiles(); track f.path) {
              <li>
                <button
                  type="button"
                  class="file"
                  [class.file--active]="f.path === selected()"
                  [attr.aria-current]="f.path === selected() ? 'true' : null"
                  (click)="open(f)"
                >
                  <okf-icon name="file" />
                  <span class="name">{{ f.path }}</span>
                  <span class="size meta">{{ (f.size_bytes / 1024) | number: '1.0-1' }} KiB</span>
                </button>
              </li>
            }
          </ul>

          @if (assetFiles().length > 0) {
            <p class="eyebrow group">assets/</p>
            <ul>
              @for (f of assetFiles(); track f.path) {
                <li>
                  <button
                    type="button"
                    class="file"
                    [class.file--active]="f.path === selected()"
                    [attr.aria-current]="f.path === selected() ? 'true' : null"
                    (click)="open(f)"
                  >
                    <okf-icon name="image" />
                    <span class="name">{{ f.path.replace('assets/', '') }}</span>
                    <span class="size meta">{{ (f.size_bytes / 1024) | number: '1.0-1' }} KiB</span>
                  </button>
                </li>
              }
            </ul>
          }
        </aside>

        <!-- --------------------------------------------- previsualizacion -- -->
        <article class="card preview">
          @if (selected()) {
            <div class="spread pv-head">
              <h2 class="pv-title"><code>{{ selected() }}</code></h2>
              <button type="button" class="btn-ghost btn--sm" (click)="raw.set(!raw())">
                {{ raw() ? 'Ver renderizado' : 'Ver fuente' }}
              </button>
            </div>

            @if (fmEntries().length > 0) {
              <table class="data fm">
                <tbody>
                  @for (entry of fmEntries(); track entry[0]) {
                    <tr>
                      <th>{{ entry[0] }}</th>
                      <td class="mono small">{{ entry[1] }}</td>
                    </tr>
                  }
                </tbody>
              </table>
            }

            @if (loading()) {
              <p class="row"><span class="spinner"></span> Cargando…</p>
            } @else if (isImage()) {
              @if (imageUrl(); as url) {
                <img [src]="url" [alt]="selected()!" class="asset-img" />
              }
            } @else if (raw()) {
              <pre>{{ content() }}</pre>
            } @else {
              <div class="md" [innerHTML]="content() | markdown"></div>
            }
          } @else {
            <p class="muted">Seleccione un fichero para previsualizarlo.</p>
          }
        </article>
      </div>
    } @else if (!error()) {
      <p class="muted top row"><span class="spinner"></span> Cargando…</p>
    }
  `,
  styles: `
    .back {
      display: inline-flex; align-items: center; gap: 0.3rem;
      color: var(--muted); text-decoration: none; font-size: var(--fs-meta); font-weight: 600;
    }
    .back:hover { color: var(--text); }
    .top { margin-top: 1rem; }

    .ident { min-width: 0; }
    .title { margin: 0 0 0.35rem; }
    .source { margin-top: 0.3rem; }
    .tiles { margin-top: 1.1rem; }
    .spread button { display: inline-flex; align-items: center; gap: 0.4rem; }

    .viewer {
      display: grid; gap: var(--gutter); margin-top: var(--gutter);
      grid-template-columns: minmax(220px, 300px) minmax(0, 1fr);
      align-items: start;
    }

    .tree { position: sticky; top: var(--gutter); }
    .tree ul { list-style: none; padding: 0; margin: 0.5rem 0 0; }
    .group { margin-top: 1.1rem; }
    .file {
      display: flex; align-items: center; gap: 0.5rem; width: 100%;
      background: transparent; border: none; text-align: left;
      padding: 0.4rem 0.55rem; border-radius: var(--radius-sm);
      color: var(--muted); font-weight: 500;
      box-shadow: inset 3px 0 0 transparent;
    }
    .file:hover { background: var(--elevated); color: var(--text); }
    /* Fichero abierto: acento, peso y barra lateral. Tres senales, para que se
       distinga tambien sin percibir el color (documento, seccion 05). */
    .file--active {
      background: var(--signal-tint);
      color: var(--signal);
      font-weight: 700;
      box-shadow: inset 3px 0 0 var(--signal);
    }
    .file okf-icon svg { width: 16px; height: 16px; }
    .file .name { overflow-wrap: anywhere; font-size: 0.87rem; flex: 1; }
    .file .size { flex: none; }

    .preview { min-height: 320px; overflow-x: auto; }
    .pv-head { border-bottom: 1px solid var(--line); padding-bottom: 0.75rem; margin-bottom: 1rem; }
    .pv-title { margin: 0; font-size: 1rem; }
    .pv-title code { color: var(--text); }
    .fm { margin: 0 0 1.25rem; width: auto; background: var(--night); border-radius: var(--radius-sm); }
    /* Las claves del front matter son DATOS del fichero, no rotulos de la
       interfaz: se muestran tal como estan escritas. El text-transform de
       table.data las ensenaria en mayusculas y el usuario leeria una clave que
       no es la que hay en el .md. */
    .fm th { width: 140px; text-transform: none; letter-spacing: 0; font-size: var(--fs-meta); }
    .asset-img { max-width: 100%; border: 1px solid var(--line); border-radius: var(--radius-sm); }

    /* Por debajo de 900 px el arbol pasa a ser una tira sobre el contenido, que
       es lo unico razonable: dos columnas de 220 px no caben en un telefono. */
    @media (max-width: 900px) {
      .viewer { grid-template-columns: minmax(0, 1fr); }
      .tree { position: static; }
    }
  `,
})
export class BundleViewerComponent implements OnInit, OnDestroy {
  readonly id = input.required<string>();

  private readonly api = inject(ApiService);

  readonly bundle = signal<Bundle | null>(null);
  readonly selected = signal<string | null>(null);
  readonly content = signal('');
  readonly imageUrl = signal<string | null>(null);
  readonly loading = signal(false);
  readonly raw = signal(false);
  readonly error = signal<string | null>(null);

  readonly rootFiles = computed(() =>
    (this.bundle()?.files ?? []).filter((f) => !f.path.startsWith('assets/')),
  );
  readonly assetFiles = computed(() =>
    (this.bundle()?.files ?? []).filter((f) => f.path.startsWith('assets/')),
  );

  readonly frontMatter = computed(() => parseFrontMatter(this.content()));
  readonly fmEntries = computed(() => Object.entries(this.frontMatter()));

  readonly isImage = computed(() => {
    const p = this.selected() ?? '';
    return /\.(png|jpe?g|gif|webp)$/i.test(p);
  });

  ngOnInit(): void {
    this.api.bundle(this.id()).subscribe({
      next: (b) => {
        this.bundle.set(b);
        // Se abre index.md por defecto: es la puerta de entrada del bundle y lo
        // primero que hay que mostrar al inspeccionarlo.
        const first = (b.files ?? []).find((f) => f.path === 'index.md') ?? b.files?.[0];
        if (first) {
          this.open(first);
        }
      },
      error: (err: unknown) => this.error.set(describeError(err)),
    });
  }

  open(f: BundleFile): void {
    this.selected.set(f.path);
    this.raw.set(false);
    this.releaseImage();

    this.loading.set(true);

    // Las imagenes se traen como blob por HttpClient para que el interceptor
    // pueda anadir la cabecera Authorization; un <img src="/api/..."> directo
    // recibiria un 401 y se veria roto.
    if (/\.(png|jpe?g|gif|webp)$/i.test(f.path)) {
      this.content.set('');
      this.api.bundleAsset(this.id(), f.path).subscribe({
        next: (blob) => {
          this.imageUrl.set(URL.createObjectURL(blob));
          this.loading.set(false);
        },
        error: (err: unknown) => {
          this.loading.set(false);
          this.error.set(describeError(err));
        },
      });
      return;
    }

    this.api.bundleFile(this.id(), f.path).subscribe({
      next: (text) => {
        this.content.set(text);
        this.loading.set(false);
      },
      error: (err: unknown) => {
        this.loading.set(false);
        this.error.set(describeError(err));
      },
    });
  }

  /** Libera la URL de objeto anterior para no filtrar memoria al navegar. */
  private releaseImage(): void {
    const url = this.imageUrl();
    if (url) {
      URL.revokeObjectURL(url);
      this.imageUrl.set(null);
    }
  }

  ngOnDestroy(): void {
    this.releaseImage();
  }

  download(): void {
    this.api.download(this.id()).subscribe({
      next: (t) => window.location.assign(t.url),
      error: (err: unknown) => this.error.set(describeError(err)),
    });
  }

  /**
   * Descarga el documento original del que salio este bundle.
   *
   * El nombre del fichero lo propone el servidor en Content-Disposition, pero un
   * enlace creado desde un blob no lo respeta, asi que se fija aqui a partir de
   * los metadatos del documento.
   */
  downloadOriginal(): void {
    const b = this.bundle();
    if (!b) {
      return;
    }
    this.api.documentContent(b.document_id).subscribe({
      next: (blob) => {
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = this.originalName();
        a.click();
        // Liberar la URL de objeto: si no, el blob queda retenido en memoria
        // mientras viva la pestana.
        URL.revokeObjectURL(url);
      },
      error: (err: unknown) => this.error.set(describeError(err)),
    });
  }

  private originalName(): string {
    const source = this.bundle()?.source_filename;
    return source && source.trim() !== '' ? source : 'documento-original';
  }
}
