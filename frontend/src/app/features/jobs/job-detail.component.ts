import { DatePipe, JsonPipe } from '@angular/common';
import { Component, OnDestroy, OnInit, computed, inject, input, signal } from '@angular/core';
import { Router, RouterLink } from '@angular/router';
import { Subscription } from 'rxjs';

import { ApiService } from '../../core/api.service';
import { Finding, JobView, isRetryable, isTerminal } from '../../core/models';
import { IconComponent } from '../../shared/icon.component';
import { StatusBadgeComponent, VerdictBadgeComponent } from '../../shared/status-badge.component';
import { describeError } from '../auth/login.component';

@Component({
  selector: 'okf-job-detail',
  standalone: true,
  imports: [
    RouterLink,
    DatePipe,
    JsonPipe,
    IconComponent,
    StatusBadgeComponent,
    VerdictBadgeComponent,
  ],
  template: `
    <a routerLink="/" class="back">
      <okf-icon name="back" />
      Volver al panel
    </a>

    @if (error()) {
      <div class="alert alert--err top">{{ error() }}</div>
    }

    @if (job(); as j) {
      <div class="stack top">
        <!-- ------------------------------------------------ resumen -------- -->
        <section class="card enter">
          <div class="spread">
            <div class="ident">
              <p class="eyebrow">Trabajo</p>
              <h1 class="title">{{ j.document_filename || 'Trabajo' }}</h1>
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

          <div class="tiles">
            <div class="tile">
              <span class="tile-label">Intento</span>
              <span class="tile-value">{{ j.attempt }} de {{ j.max_attempts }}</span>
            </div>
            <div class="tile">
              <span class="tile-label">Reclamaciones</span>
              <span class="tile-value">{{ j.claim_count }}</span>
            </div>
            <div class="tile">
              <span class="tile-label">Creado</span>
              <span class="tile-value">{{ j.created_at | date: 'dd/MM/yy HH:mm:ss' }}</span>
            </div>
            <div class="tile">
              <span class="tile-label">Finalizado</span>
              <span class="tile-value">
                {{ j.finished_at ? (j.finished_at | date: 'dd/MM/yy HH:mm:ss') : '—' }}
              </span>
            </div>
            @if (j.okf_score !== undefined && j.okf_score !== null) {
              <div class="tile">
                <span class="tile-label">Conformidad OKF</span>
                <span class="tile-value">{{ j.okf_score }}/100 · grado {{ j.okf_grade }}</span>
              </div>
            }
          </div>

          @if (j.error_message) {
            <div class="alert alert--err">
              <strong>{{ j.error_code }}</strong> — {{ j.error_message }}
            </div>
          }

          <div class="row actions">
            @if (j.bundle_id) {
              <a class="btn-primary link-btn" [routerLink]="['/bundles', j.bundle_id]">
                Abrir el bundle
              </a>
              <button type="button" class="btn-secondary" (click)="download(j.bundle_id!)">
                <okf-icon name="download" />
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
            <p class="eyebrow">Validacion</p>
            <h2 class="h2-flush">Validacion del bundle</h2>
            <p class="muted note">
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
                    <span class="fmsg">
                      {{ f.message }}
                      @if (f.path) { <code class="small">{{ f.path }}</code> }
                    </span>
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
                    <span class="fmsg">
                      {{ f.message }}
                      @if (f.path) { <code class="small">{{ f.path }}</code> }
                    </span>
                  </li>
                }
              </ul>
            }

            @if (okfFindings().length > 0) {
              <h3>Conformidad OKF (informativo)</h3>
              <!-- El eje informativo usa el acento secundario: es lo destacado
                   que no bloquea, y el documento reserva el violeta justo a eso. -->
              <ul class="findings">
                @for (f of okfFindings(); track f.code) {
                  <li>
                    <span class="badge badge--pulse">{{ f.code }}</span>
                    <span class="fmsg">{{ f.message }}</span>
                  </li>
                }
              </ul>
            }

            @if (platformErrors().length === 0 && platformWarnings().length === 0) {
              <p class="badge badge--ok clean">Todas las reglas obligatorias se superaron</p>
            }
          </section>
        }

        <!-- ------------------------------------------- linea de tiempo ----- -->
        <section class="card">
          <p class="eyebrow">Traza</p>
          <h2 class="h2-flush">Linea de tiempo</h2>
          <p class="muted note">
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
    .h2-flush { margin: 0 0 0.4rem; }
    .note { margin: 0 0 1.1rem; max-width: 78ch; }
    .tiles { margin-top: 1.1rem; }
    .actions { margin-top: 1.15rem; }
    .actions button { display: inline-flex; align-items: center; gap: 0.4rem; }
    .clean { margin-top: 0.5rem; }

    .findings { list-style: none; padding: 0; margin: 0.5rem 0 0; }
    .findings li {
      display: flex; align-items: flex-start; gap: 0.6rem;
      padding: 0.55rem 0; border-bottom: 1px solid var(--line); font-size: 0.9rem;
    }
    .findings li:last-child { border-bottom: none; }
    .fmsg { min-width: 0; }

    /* Linea de tiempo con carril: la regla vertical y el punto de cada evento
       explican la continuidad de la traza mejor que una lista suelta, y no
       dependen del color para leerse. */
    .timeline { list-style: none; padding: 0; margin: 0.5rem 0 0; }
    .timeline li {
      position: relative;
      display: grid; grid-template-columns: 92px 210px minmax(0, 1fr); gap: 0.7rem;
      padding: 0.4rem 0 0.4rem 1.15rem;
      align-items: baseline;
      border-left: 1px solid var(--line);
    }
    .timeline li::before {
      content: "";
      position: absolute; left: -4px; top: 0.95em;
      width: 7px; height: 7px; border-radius: 50%;
      background: var(--elevated);
      border: 1px solid var(--line-strong);
    }
    /* Solo el ultimo evento lleva el acento: es el estado actual del trabajo. */
    .timeline li:last-child::before { background: var(--signal); border-color: var(--signal); }
    .timeline li:first-child { padding-top: 0.2rem; }
    .timeline .type { font-weight: 600; font-size: 0.87rem; }
    .timeline .detail { overflow-wrap: anywhere; }
    .alert { margin-top: 0.9rem; }

    @media (max-width: 720px) {
      .timeline li { grid-template-columns: minmax(0, 1fr); gap: 0.15rem; }
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
