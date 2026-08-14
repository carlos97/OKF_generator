import { DecimalPipe } from '@angular/common';
import { Component, OnDestroy, OnInit, computed, inject, input, signal } from '@angular/core';
import { RouterLink } from '@angular/router';

import { ApiService } from '../../core/api.service';
import { Bundle, BundleFile } from '../../core/models';
import { MarkdownPipe, parseFrontMatter } from '../../shared/markdown.pipe';
import { describeError } from '../auth/login.component';

@Component({
  selector: 'okf-bundle-viewer',
  standalone: true,
  imports: [RouterLink, DecimalPipe, MarkdownPipe],
  template: `
    <a routerLink="/" class="small">&larr; Volver al panel</a>

    @if (error()) {
      <div class="alert alert--err" style="margin-top:1rem">{{ error() }}</div>
    }

    @if (bundle(); as b) {
      <section class="card" style="margin-top:1rem">
        <div class="spread">
          <div>
            <h1 style="margin-bottom:0.35rem">Bundle OKF</h1>
            <code class="small">{{ b.id }}</code>
            @if (b.source_filename) {
              <div class="muted small" style="margin-top:0.25rem">
                generado desde <strong>{{ b.source_filename }}</strong>
              </div>
            }
          </div>
          <div class="row">
            <span class="badge badge--ok">Publicado</span>
            <button type="button" class="btn-secondary" (click)="downloadOriginal()">
              Descargar original
            </button>
            <button type="button" class="btn-primary" (click)="download()">
              Descargar ZIP
            </button>
          </div>
        </div>

        <div class="grid">
          <div><span class="muted small">Unidades</span><br />{{ b.unit_count }}</div>
          <div>
            <span class="muted small">Tamano</span><br />
            {{ (b.total_bytes / 1024) | number: '1.0-1' }} KiB
          </div>
          <div><span class="muted small">Ficheros</span><br />{{ (b.files ?? []).length }}</div>
        </div>
      </section>

      <div class="viewer">
        <!-- --------------------------------------------- arbol de ficheros -- -->
        <aside class="card tree">
          <h2 style="margin-top:0">Contenido</h2>
          <ul>
            @for (f of rootFiles(); track f.path) {
              <li>
                <button
                  type="button"
                  class="file"
                  [class.file--active]="f.path === selected()"
                  (click)="open(f)"
                >
                  <span class="name">{{ f.path }}</span>
                  <span class="size muted small">{{ (f.size_bytes / 1024) | number: '1.0-1' }} KiB</span>
                </button>
              </li>
            }
          </ul>

          @if (assetFiles().length > 0) {
            <h3>assets/</h3>
            <ul>
              @for (f of assetFiles(); track f.path) {
                <li>
                  <button
                    type="button"
                    class="file"
                    [class.file--active]="f.path === selected()"
                    (click)="open(f)"
                  >
                    <span class="name">{{ f.path.replace('assets/', '') }}</span>
                    <span class="size muted small">{{ (f.size_bytes / 1024) | number: '1.0-1' }} KiB</span>
                  </button>
                </li>
              }
            </ul>
          }
        </aside>

        <!-- --------------------------------------------- previsualizacion -- -->
        <article class="card preview">
          @if (selected()) {
            <div class="spread">
              <h2 style="margin:0"><code>{{ selected() }}</code></h2>
              <button type="button" class="btn-ghost small-btn" (click)="raw.set(!raw())">
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
      <p class="muted" style="margin-top:1rem">Cargando…</p>
    }
  `,
  styles: `
    .grid {
      display: grid; gap: 0.9rem; margin-top: 1rem;
      grid-template-columns: repeat(auto-fit, minmax(120px, 1fr));
    }
    .viewer {
      display: grid; gap: 1rem; margin-top: 1rem;
      grid-template-columns: minmax(220px, 300px) 1fr;
      align-items: start;
    }
    .tree ul { list-style: none; padding: 0; margin: 0 0 0.5rem; }
    .tree h3 { font-size: 0.85rem; color: var(--text-2); margin: 1rem 0 0.4rem; }
    .file {
      display: flex; justify-content: space-between; gap: 0.5rem; width: 100%;
      background: transparent; border: none; text-align: left;
      padding: 0.35rem 0.5rem; border-radius: 6px; color: inherit;
    }
    .file:hover { background: var(--surface-3); }
    .file--active { background: var(--accent-soft); color: var(--accent); font-weight: 600; }
    .file .name { overflow-wrap: anywhere; font-size: 0.87rem; }
    .preview { min-height: 300px; overflow-x: auto; }
    .fm { margin: 0.75rem 0 1rem; width: auto; }
    .fm th { width: 140px; }
    .asset-img { max-width: 100%; border: 1px solid var(--border); border-radius: 6px; }
    .small-btn { padding: 0.25rem 0.55rem; font-size: 0.8rem; }
    @media (max-width: 860px) {
      .viewer { grid-template-columns: 1fr; }
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
