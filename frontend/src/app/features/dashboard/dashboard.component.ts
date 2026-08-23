import { HttpEventType } from '@angular/common/http';
import { Component, OnInit, computed, inject, signal } from '@angular/core';
import { DatePipe, DecimalPipe } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { Router, RouterLink } from '@angular/router';

import { ActivityService } from '../../core/activity.service';
import { ApiService } from '../../core/api.service';
import { Job, Limits, isRetryable } from '../../core/models';
import { IconComponent } from '../../shared/icon.component';
import {
  StatusBadgeComponent,
  VerdictBadgeComponent,
  statusLabel,
} from '../../shared/status-badge.component';
import { describeError } from '../auth/login.component';

@Component({
  selector: 'okf-dashboard',
  standalone: true,
  imports: [
    RouterLink,
    DatePipe,
    DecimalPipe,
    FormsModule,
    IconComponent,
    StatusBadgeComponent,
    VerdictBadgeComponent,
  ],
  template: `
    <div class="stack">
      <!-- ---------------------------------------------------- carga -------- -->
      <section class="card anchor" id="convertir">
        <p class="eyebrow">Convertir</p>
        <h1>Suba un documento</h1>
        <p class="muted intro">
          La respuesta llega de inmediato y la conversion sigue en un worker
          aparte. Puede cerrar esta pagina sin interrumpirla.
        </p>

        <div
          class="dropzone"
          [class.dropzone--over]="dragging()"
          (dragover)="onDragOver($event)"
          (dragleave)="dragging.set(false)"
          (drop)="onDrop($event)"
          (click)="picker.click()"
          (keydown.enter)="picker.click()"
          tabindex="0"
          role="button"
          aria-label="Seleccionar documento"
        >
          <input
            #picker
            type="file"
            hidden
            [accept]="accept()"
            (change)="onPick($event)"
          />

          @if (uploading()) {
            <p class="row center"><span class="spinner"></span> Subiendo… {{ progress() }}%</p>
            <div class="bar up"><div class="bar-fill" [style.width.%]="progress()"></div></div>
          } @else {
            <span class="dz-icon" aria-hidden="true"><okf-icon name="upload" /></span>
            <p class="dz-title">Arrastre un documento aqui o pulse para elegirlo</p>
            <p class="meta">
              Formatos aceptados: {{ (limits()?.allowed_extensions ?? []).join(', ') }}
              @if (limits()) {
                · maximo {{ (limits()!.max_upload_bytes / 1048576) | number: '1.0-0' }} MiB
              }
            </p>
          }
        </div>

        @if (error()) {
          <div class="alert alert--err">{{ error() }}</div>
        }
        @if (lastAccepted()) {
          <div class="alert alert--info">
            Trabajo <code>{{ lastAccepted() }}</code> aceptado y encolado. La
            respuesta llego de inmediato: la conversion se ejecuta en un worker
            aparte y puede cerrar esta pagina sin interrumpirla.
            <a [routerLink]="['/jobs', lastAccepted()]">Ver el trabajo</a>
          </div>
        }
      </section>

      <!-- --------------------------------------------------- trabajos ------ -->
      <section class="card anchor" id="trabajos">
        <div class="spread head">
          <div>
            <p class="eyebrow">Actividad</p>
            <h2 class="h2-flush">Trabajos</h2>
          </div>

          <!-- Buscador prominente con el icono dentro del campo. Filtra en
               cliente sobre la lista que ya esta en memoria: pedir al servidor
               un endpoint de busqueda para una lista propia de decenas de filas
               anadiria una peticion por tecla sin ganar nada. -->
          <div class="search">
            <span class="search-icon"><okf-icon name="search" /></span>
            <input
              type="search"
              name="q"
              placeholder="Buscar por nombre, estado o identificador"
              aria-label="Buscar trabajos"
              [ngModel]="query()"
              (ngModelChange)="query.set($event)"
            />
          </div>
        </div>

        <div class="tiles">
          <div class="tile">
            <span class="tile-label">Total</span>
            <span class="tile-value">{{ all().length }}</span>
          </div>
          <div class="tile">
            <span class="tile-label">En vuelo</span>
            <span class="tile-value">{{ activity.pending() }}</span>
          </div>
          <div class="tile">
            <span class="tile-label">Publicados</span>
            <span class="tile-value">{{ published() }}</span>
          </div>
        </div>

        @if (all().length === 0) {
          <p class="muted empty">Aun no hay trabajos. Suba un documento para empezar.</p>
        } @else if (jobs().length === 0) {
          <p class="muted empty">Ningun trabajo coincide con «{{ query() }}».</p>
        } @else {
          <div class="table-wrap">
            <table class="data">
              <thead>
                <tr>
                  <th>Documento</th>
                  <th>Creado</th>
                  <th>Estado</th>
                  <th>Veredicto</th>
                  <th>OKF</th>
                  <th>Intento</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                @for (job of jobs(); track job.id) {
                  <tr>
                    <td class="doc-name" [title]="job.document_filename ?? ''">
                      {{ job.document_filename || '(sin nombre)' }}
                    </td>
                    <td class="meta">{{ job.created_at | date: 'dd/MM HH:mm:ss' }}</td>
                    <td><okf-status-badge [status]="job.status" /></td>
                    <td>
                      @if (job.result_class) {
                        <okf-verdict-badge [verdict]="job.result_class" />
                      } @else {
                        <span class="muted small">—</span>
                      }
                    </td>
                    <td class="meta">
                      @if (job.okf_score !== undefined && job.okf_score !== null) {
                        {{ job.okf_score }}/100 ({{ job.okf_grade }})
                      } @else {
                        <span class="muted">—</span>
                      }
                    </td>
                    <td class="meta">{{ job.attempt }}/{{ job.max_attempts }}</td>
                    <td>
                      <div class="row actions">
                        <a class="small" [routerLink]="['/jobs', job.id]">Detalle</a>
                        @if (job.bundle_id) {
                          <a class="small" [routerLink]="['/bundles', job.bundle_id]">Bundle</a>
                          <button
                            type="button"
                            class="btn-secondary btn--sm"
                            (click)="download(job.bundle_id!)"
                          >
                            <okf-icon name="download" />
                            ZIP
                          </button>
                        }
                        @if (canRetry(job)) {
                          <button
                            type="button"
                            class="btn-secondary btn--sm"
                            (click)="retry(job)"
                          >
                            Reintentar
                          </button>
                        }
                      </div>
                    </td>
                  </tr>
                }
              </tbody>
            </table>
          </div>
        }
      </section>
    </div>
  `,
  styles: `
    /* Los destinos de la navegacion lateral e inferior. El margen de scroll
       evita que la cabecera fija de movil tape el titulo del bloque al saltar. */
    .anchor { scroll-margin-top: 72px; }

    .intro { max-width: 62ch; margin: 0 0 1.25rem; }
    .head { align-items: flex-end; margin-bottom: 1.1rem; }
    .h2-flush { margin: 0; }
    .head .search { width: min(360px, 100%); }
    .empty { margin: 1.1rem 0 0; }
    .tiles { margin-bottom: 1.1rem; }

    /* Los nombres de fichero pueden ser largos: se acotan con puntos
       suspensivos y el nombre completo queda en el title del td. */
    .doc-name {
      max-width: 18rem;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font-weight: 600;
    }
    .actions { gap: 0.5rem; flex-wrap: nowrap; }
    .actions button { display: inline-flex; align-items: center; gap: 0.3rem; }
    .actions okf-icon svg { width: 16px; height: 16px; }

    .dropzone {
      border: 1px dashed var(--line-strong);
      border-radius: var(--radius);
      padding: 2.25rem 1rem;
      text-align: center;
      cursor: pointer;
      background: var(--night);
      transition: border-color var(--t-micro) var(--ease), background var(--t-micro) var(--ease);
    }
    .dropzone:hover { border-color: var(--signal-line); }
    /* Confirmacion del arrastre: acento en el borde y un tinte, no un relleno
       verde de la zona entera. */
    .dropzone--over { border-color: var(--signal); background: var(--signal-tint); }

    .dz-icon {
      display: inline-flex;
      padding: 0.7rem;
      border-radius: 999px;
      background: var(--elevated);
      color: var(--signal);
      margin-bottom: 0.75rem;
    }
    .dz-icon okf-icon svg { width: 24px; height: 24px; }
    .dz-title { margin: 0 0 0.35rem; font-weight: 600; }
    .center { justify-content: center; }
    .up { margin-top: 0.8rem; }
    .alert { margin-top: 0.9rem; }

    @media (max-width: 720px) {
      .head { align-items: stretch; }
      .head .search { width: 100%; }
      .doc-name { max-width: 11rem; }
    }
  `,
})
export class DashboardComponent implements OnInit {
  private readonly api = inject(ApiService);
  private readonly router = inject(Router);

  /**
   * El sondeo ya no vive aqui: lo mantiene ActivityService, que es tambien
   * quien alimenta la barra persistente y la lista de la barra lateral. Con un
   * sondeo por pantalla habria dos temporizadores pidiendo la misma lista cada
   * tres segundos.
   */
  readonly activity = inject(ActivityService);

  readonly all = this.activity.jobs;
  readonly query = signal('');

  readonly limits = signal<Limits | null>(null);
  readonly dragging = signal(false);
  readonly uploading = signal(false);
  readonly progress = signal(0);
  readonly error = signal<string | null>(null);
  readonly lastAccepted = signal<string | null>(null);

  /** Filtro por nombre, estado visible o identificador. */
  readonly jobs = computed(() => {
    const q = this.query().trim().toLowerCase();
    const items = this.all();
    if (q === '') {
      return items;
    }
    return items.filter(
      (j) =>
        (j.document_filename ?? '').toLowerCase().includes(q) ||
        j.id.toLowerCase().includes(q) ||
        statusLabel(j.status).toLowerCase().includes(q),
    );
  });

  readonly published = computed(
    () => this.all().filter((j) => Boolean(j.bundle_id)).length,
  );

  ngOnInit(): void {
    this.api.limits().subscribe({ next: (l) => this.limits.set(l) });
  }

  accept(): string {
    return (this.limits()?.allowed_extensions ?? []).join(',');
  }

  canRetry(job: Job): boolean {
    return isRetryable(job.status);
  }

  onDragOver(e: DragEvent): void {
    e.preventDefault();
    this.dragging.set(true);
  }

  onDrop(e: DragEvent): void {
    e.preventDefault();
    this.dragging.set(false);
    const file = e.dataTransfer?.files?.[0];
    if (file) {
      this.upload(file);
    }
  }

  onPick(e: Event): void {
    const input = e.target as HTMLInputElement;
    const file = input.files?.[0];
    if (file) {
      this.upload(file);
    }
    input.value = '';
  }

  /**
   * Valida en cliente con los MISMOS limites que aplica el servidor (los publica
   * /meta/limits). La validacion en cliente es comodidad, no seguridad: el
   * servidor vuelve a comprobarlo todo.
   */
  private upload(file: File): void {
    this.error.set(null);
    this.lastAccepted.set(null);

    const lim = this.limits();
    if (lim) {
      const ext = file.name.slice(file.name.lastIndexOf('.')).toLowerCase();
      if (!lim.allowed_extensions.includes(ext)) {
        this.error.set(
          `Extension ${ext} no aceptada. Formatos: ${lim.allowed_extensions.join(', ')}`,
        );
        return;
      }
      if (file.size === 0) {
        this.error.set('El fichero esta vacio.');
        return;
      }
      if (file.size > lim.max_upload_bytes) {
        this.error.set(
          `El fichero pesa ${(file.size / 1048576).toFixed(1)} MiB y el maximo es ` +
            `${(lim.max_upload_bytes / 1048576).toFixed(0)} MiB.`,
        );
        return;
      }
    }

    this.uploading.set(true);
    this.progress.set(0);

    this.api.upload(file).subscribe({
      next: (event) => {
        if (event.type === HttpEventType.UploadProgress && event.total) {
          this.progress.set(Math.round((100 * event.loaded) / event.total));
        }
        if (event.type === HttpEventType.Response && event.body) {
          this.uploading.set(false);
          this.lastAccepted.set(event.body.job_id);
        }
      },
      error: (err: unknown) => {
        this.uploading.set(false);
        this.error.set(describeError(err));
      },
    });
  }

  retry(job: Job): void {
    this.api.retryJob(job.id).subscribe({
      next: (child) => void this.router.navigate(['/jobs', child.id]),
      error: (err: unknown) => this.error.set(describeError(err)),
    });
  }

  /**
   * El ticket se pide EN EL MOMENTO DEL CLIC, no al cargar la lista: tiene un
   * TTL corto y uno pedido antes ya habria caducado. Despues se navega a la URL,
   * de modo que el navegador escribe directo a disco y el ZIP no pasa por la
   * memoria de la pestana.
   */
  download(bundleId: string): void {
    this.api.download(bundleId).subscribe({
      next: (t) => window.location.assign(t.url),
      error: (err: unknown) => this.error.set(describeError(err)),
    });
  }
}
