import { DatePipe, JsonPipe } from '@angular/common';
import { Component, OnDestroy, OnInit, computed, inject, input, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { Subscription } from 'rxjs';

import { ApiService } from '../../core/api.service';
import { Finding, JobView, isRetryable, isTerminal } from '../../core/models';
import { StatusBadgeComponent, VerdictBadgeComponent } from '../../shared/status-badge.component';
import { describeError } from '../auth/login.component';

@Component({
  selector: 'okf-job-detail',
  standalone: true,
  imports: [RouterLink, DatePipe, JsonPipe, StatusBadgeComponent, VerdictBadgeComponent],
  template: `
    <a routerLink="/" class="small">&larr; Volver al panel</a>

    @if (error()) {
      <div class="alert alert--err" style="margin-top:1rem">{{ error() }}</div>
    }

    @if (job(); as j) {
      <div class="stack" style="margin-top:1rem">
        <!-- ------------------------------------------------ resumen -------- -->
        <section class="card">
          <div class="spread">
            <div>
              <h1 style="margin-bottom:0.35rem">
                {{ j.document_filename || 'Trabajo' }}
              </h1>
              <code class="small">{{ j.id }}</code>
            </div>
            <div class="row">
              <okf-status-badge [status]="j.status" />
              @if (j.result_class) {
                <okf-verdict-badge [verdict]="j.result_class" />
              }
              @if (!isDone()) {
                <span class="spinner" aria-label="en curso"></span>
              }
            </div>
          </div>

          <div class="grid">
            <div><span class="muted small">Intento</span><br />{{ j.attempt }} de {{ j.max_attempts }}</div>
            <div><span class="muted small">Reclamaciones</span><br />{{ j.claim_count }}</div>
            <div><span class="muted small">Creado</span><br />{{ j.created_at | date: 'dd/MM/yy HH:mm:ss' }}</div>
            <div>
              <span class="muted small">Finalizado</span><br />
              {{ j.finished_at ? (j.finished_at | date: 'dd/MM/yy HH:mm:ss') : '—' }}
            </div>
            @if (j.okf_score !== undefined && j.okf_score !== null) {
              <div>
                <span class="muted small">Conformidad OKF</span><br />
                {{ j.okf_score }}/100 · grado {{ j.okf_grade }}
              </div>
            }
          </div>

          @if (j.error_message) {
            <div class="alert alert--err">
              <strong>{{ j.error_code }}</strong> — {{ j.error_message }}
            </div>
          }

          <div class="row" style="margin-top:1rem">
            @if (j.bundle_id) {
              <a class="btn-primary link-btn" [routerLink]="['/bundles', j.bundle_id]">
                Abrir el bundle
              </a>
              <button type="button" class="btn-secondary" (click)="download(j.bundle_id!)">
                Descargar ZIP
              </button>
            }
            @if (canCancel()) {
              <button type="button" class="btn-ghost" (click)="cancel(j)">Cancelar</button>
            }
            @if (isRetryable(j.status)) {
              <button type="button" class="btn-secondary" (click)="retry(j)">
                Reintentar
              </button>
            }
            <button type="button" class="btn-ghost" (click)="replay(j)">
              Reinyectar en la cola (x2)
            </button>
          </div>

          @if (replayNote()) {
            <div class="alert alert--info">{{ replayNote() }}</div>
          }
        </section>

        <!-- ------------------------------------ validacion y hallazgos ----- -->
        @if (j.validation_report; as rep) {
          <section class="card">
            <h2 style="margin-top:0">Validacion del bundle</h2>
            <p class="muted small">
              Se evaluaron {{ rep.rules_evaluated }} reglas. La validez de
              plataforma decide si el bundle se publica; la conformidad OKF es una
              medida de calidad que no bloquea la publicacion.
            </p>

            @if (platformErrors().length > 0) {
              <h3>Errores que impidieron la publicacion</h3>
              <ul class="findings">
                @for (f of platformErrors(); track f.code + f.path) {
                  <li>
                    <span class="badge badge--err">{{ f.code }}</span>
                    {{ f.message }}
                    @if (f.path) { <code class="small">{{ f.path }}</code> }
                  </li>
                }
              </ul>
            }

            @if (platformWarnings().length > 0) {
              <h3>Advertencias</h3>
              <ul class="findings">
                @for (f of platformWarnings(); track f.code + f.path) {
                  <li>
                    <span class="badge badge--warn">{{ f.code }}</span>
                    {{ f.message }}
                    @if (f.path) { <code class="small">{{ f.path }}</code> }
                  </li>
                }
              </ul>
            }

            @if (okfFindings().length > 0) {
              <h3>Conformidad OKF (informativo)</h3>
              <ul class="findings">
                @for (f of okfFindings(); track f.code) {
                  <li>
                    <span class="badge badge--idle">{{ f.code }}</span>
                    {{ f.message }}
                  </li>
                }
              </ul>
            }

            @if (platformErrors().length === 0 && platformWarnings().length === 0) {
              <p class="badge badge--ok">Todas las reglas obligatorias se superaron</p>
            }
          </section>
        }

        <!-- ------------------------------------------- linea de tiempo ----- -->
        <section class="card">
          <h2 style="margin-top:0">Linea de tiempo</h2>
          <p class="muted small">
            Traza auditable del trabajo. Los eventos los escribe el worker en la
            base de datos, de modo que sobreviven al reinicio de cualquier
            contenedor.
          </p>
          <ol class="timeline">
            @for (e of j.events; track e.id) {
              <li>
                <span class="ts small mono">{{ e.created_at | date: 'HH:mm:ss.SSS' }}</span>
                <span class="type">{{ e.type }}</span>
                @if (e.detail) {
                  <span class="detail small muted">{{ e.detail | json }}</span>
                }
              </li>
            }
          </ol>
        </section>
      </div>
    } @else if (!error()) {
      <p class="muted" style="margin-top:1rem">Cargando…</p>
    }
  `,
  styles: `
    .grid {
      display: grid; gap: 0.9rem; margin-top: 1rem;
      grid-template-columns: repeat(auto-fit, minmax(140px, 1fr));
    }
    .link-btn { display: inline-block; text-decoration: none; }
    .findings { list-style: none; padding: 0; margin: 0.5rem 0 0; }
    .findings li {
      display: flex; align-items: flex-start; gap: 0.5rem;
      padding: 0.4rem 0; border-bottom: 1px solid var(--border); font-size: 0.9rem;
    }
    .timeline { list-style: none; padding: 0; margin: 0.5rem 0 0; }
    .timeline li {
      display: grid; grid-template-columns: 90px 200px 1fr; gap: 0.6rem;
      padding: 0.35rem 0; border-bottom: 1px solid var(--border); align-items: baseline;
    }
    .timeline .type { font-weight: 600; font-size: 0.87rem; }
    .timeline .detail { overflow-wrap: anywhere; }
    .alert { margin-top: 0.9rem; }
    @media (max-width: 720px) {
      .timeline li { grid-template-columns: 1fr; gap: 0.15rem; }
    }
  `,
})
export class JobDetailComponent implements OnInit, OnDestroy {
  /** Llega por withComponentInputBinding desde la ruta /jobs/:id */
  readonly id = input.required<string>();

  private readonly api = inject(ApiService);
  private readonly router = inject(Router);
  private poll?: Subscription;

  readonly job = signal<JobView | null>(null);
  readonly error = signal<string | null>(null);
  readonly replayNote = signal<string | null>(null);

  readonly isDone = computed(() => {
    const j = this.job();
    return j ? isTerminal(j.status) : false;
  });

  readonly canCancel = computed(() => {
    const j = this.job();
    return !!j && (j.status === 'queued' || j.status === 'running');
  });

  readonly platformErrors = computed(() => this.findings('platform', 'error'));
  readonly platformWarnings = computed(() => this.findings('platform', 'warning'));
  readonly okfFindings = computed(() =>
    (this.job()?.validation_report?.findings ?? []).filter((f) => f.axis === 'okf'),
  );

  readonly isRetryable = isRetryable;

  ngOnInit(): void {
    // El sondeo se detiene solo al llegar a un estado terminal (takeWhile con
    // inclusive), de modo que la pagina no consulta indefinidamente.
    this.poll = this.api.pollJob(this.id(), 1500).subscribe({
      next: (j) => this.job.set(j),
      error: (err: unknown) => this.error.set(describeError(err)),
    });
  }

  ngOnDestroy(): void {
    this.poll?.unsubscribe();
  }

  private findings(axis: 'platform' | 'okf', severity: string): Finding[] {
    return (this.job()?.validation_report?.findings ?? []).filter(
      (f) => f.axis === axis && f.severity === severity,
    );
  }

  cancel(j: JobView): void {
    this.api.cancelJob(j.id).subscribe({
      next: () => this.restartPolling(),
      error: (err: unknown) => this.error.set(describeError(err)),
    });
  }

  retry(j: JobView): void {
    this.api.retryJob(j.id).subscribe({
      next: (child) => void this.router.navigate(['/jobs', child.id]),
      error: (err: unknown) => this.error.set(describeError(err)),
    });
  }

  /**
   * Reinyecta el mismo mensaje en la cola. Es la prueba en vivo de que una
   * entrega duplicada NO produce un segundo bundle: el trabajo sigue teniendo un
   * unico bundle y la linea de tiempo registra la entrega descartada.
   */
  replay(j: JobView): void {
    this.replayNote.set(null);
    this.api.replayJob(j.id, 2).subscribe({
      next: (r) =>
        this.replayNote.set(
          `Se reinyectaron ${r.republished} mensajes del mismo trabajo. ` +
            `Compruebe en la linea de tiempo que se descartan como entregas ` +
            `duplicadas y que sigue habiendo un solo bundle.`,
        ),
      error: (err: unknown) =>
        this.error.set(
          describeError(err) + ' (esta herramienta requiere DEV_TOOLS=true)',
        ),
    });
  }

  download(bundleId: string): void {
    this.api.download(bundleId).subscribe({
      next: (t) => window.location.assign(t.url),
      error: (err: unknown) => this.error.set(describeError(err)),
    });
  }

  private restartPolling(): void {
    this.poll?.unsubscribe();
    this.poll = this.api.pollJob(this.id(), 1500).subscribe({
      next: (j) => this.job.set(j),
      error: (err: unknown) => this.error.set(describeError(err)),
    });
  }
}
