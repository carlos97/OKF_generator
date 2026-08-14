import { HttpEventType } from '@angular/common/http';
import { Component, OnDestroy, OnInit, inject, signal } from '@angular/core';
import { DatePipe, DecimalPipe } from '@angular/common';
import { Router, RouterLink } from '@angular/router';
import { Subscription } from 'rxjs';

import { ApiService } from '../../core/api.service';
import { Job, Limits, isRetryable } from '../../core/models';
import { StatusBadgeComponent, VerdictBadgeComponent } from '../../shared/status-badge.component';
import { describeError } from '../auth/login.component';

@Component({
  selector: 'okf-dashboard',
  standalone: true,
  imports: [RouterLink, DatePipe, DecimalPipe, StatusBadgeComponent, VerdictBadgeComponent],
  template: `
    <div class="stack">
      <!-- ---------------------------------------------------- carga -------- -->
      <section class="card">
        <h1>Convertir un documento</h1>

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
            <p class="row"><span class="spinner"></span> Subiendo… {{ progress() }}%</p>
            <div class="bar"><div class="bar-fill" [style.width.%]="progress()"></div></div>
          } @else {
            <p class="dz-title">Arrastre un documento aqui o pulse para elegirlo</p>
            <p class="muted small">
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
      <section class="card">
        <div class="spread">
          <h2 style="margin:0">Trabajos</h2>
          <span class="muted small">
            Se actualiza automaticamente cada 3 s
            <span class="spinner" aria-hidden="true"></span>
          </span>
        </div>

        @if (jobs().length === 0) {
          <p class="muted">Aun no hay trabajos. Suba un documento para empezar.</p>
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
                    <td class="small">{{ job.created_at | date: 'dd/MM HH:mm:ss' }}</td>
                    <td><okf-status-badge [status]="job.status" /></td>
                    <td>
                      @if (job.result_class) {
                        <okf-verdict-badge [verdict]="job.result_class" />
                      } @else {
                        <span class="muted small">—</span>
                      }
                    </td>
                    <td class="small">
                      @if (job.okf_score !== undefined && job.okf_score !== null) {
                        {{ job.okf_score }}/100 ({{ job.okf_grade }})
                      } @else {
                        <span class="muted">—</span>
                      }
                    </td>
                    <td class="small">{{ job.attempt }}/{{ job.max_attempts }}</td>
                    <td>
                      <div class="row">
                        <a class="small" [routerLink]="['/jobs', job.id]">Detalle</a>
                        @if (job.bundle_id) {
                          <a class="small" [routerLink]="['/bundles', job.bundle_id]">Bundle</a>
                          <button
                            type="button"
                            class="btn-secondary small-btn"
                            (click)="download(job.bundle_id!)"
                          >
                            Descargar ZIP
                          </button>
                        }
                        @if (canRetry(job)) {
                          <button
                            type="button"
                            class="btn-secondary small-btn"
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
    /* Los nombres de fichero pueden ser largos: se acotan con puntos
       suspensivos y el nombre completo queda en el title del td. */
    .doc-name {
      max-width: 18rem;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
      font-weight: 500;
    }
    .dropzone {
      border: 2px dashed var(--border);
      border-radius: var(--radius);
      padding: 2rem 1rem;
      text-align: center;
      cursor: pointer;
      background: var(--surface-1);
      transition: border-color 0.15s ease, background 0.15s ease;
    }
    .dropzone--over { border-color: var(--accent); background: var(--accent-soft); }
    .dz-title { margin: 0 0 0.35rem; font-weight: 600; }
    .bar { height: 6px; background: var(--surface-3); border-radius: 999px; overflow: hidden; margin-top: 0.6rem; }
    .bar-fill { height: 100%; background: var(--accent); transition: width 0.2s ease; }
    .alert { margin-top: 0.9rem; }
    .small-btn { padding: 0.25rem 0.55rem; font-size: 0.8rem; }
  `,
})
export class DashboardComponent implements OnInit, OnDestroy {
  private readonly api = inject(ApiService);
  private readonly router = inject(Router);

  private poll?: Subscription;

  readonly jobs = signal<Job[]>([]);
  readonly limits = signal<Limits | null>(null);
  readonly dragging = signal(false);
  readonly uploading = signal(false);
  readonly progress = signal(0);
  readonly error = signal<string | null>(null);
  readonly lastAccepted = signal<string | null>(null);

  ngOnInit(): void {
    this.api.limits().subscribe({ next: (l) => this.limits.set(l) });
    // Sondeo: cualquier replica de la API puede responder cualquier GET, asi que
    // el seguimiento en vivo no requiere estado en el servidor.
    this.poll = this.api.pollJobs(3000).subscribe({
      next: (items) => this.jobs.set(items),
    });
  }

  ngOnDestroy(): void {
    this.poll?.unsubscribe();
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
