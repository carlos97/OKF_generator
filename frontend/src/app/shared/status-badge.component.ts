import { Component, computed, input } from '@angular/core';

import { JobStatus, ResultClass } from '../core/models';

/** Distintivo del ESTADO del trabajo. */
@Component({
  selector: 'okf-status-badge',
  standalone: true,
  template: `<span class="badge" [class]="cls()">{{ label() }}</span>`,
})
export class StatusBadgeComponent {
  readonly status = input.required<JobStatus>();

  readonly label = computed(() => {
    switch (this.status()) {
      case 'queued': return 'En cola';
      case 'running': return 'Procesando';
      case 'canceling': return 'Cancelando';
      case 'succeeded': return 'Completado';
      case 'invalid': return 'Bundle invalido';
      case 'failed': return 'Fallido';
      case 'dead': return 'Agotados los reintentos';
      case 'canceled': return 'Cancelado';
    }
  });

  readonly cls = computed(() => {
    switch (this.status()) {
      case 'succeeded': return 'badge--ok';
      case 'invalid': case 'failed': case 'dead': return 'badge--err';
      case 'canceling': case 'canceled': return 'badge--warn';
      default: return 'badge--idle';
    }
  });
}

/**
 * Distintivo del VEREDICTO del bundle, que es un eje distinto del estado del
 * trabajo: una conversion puede terminar correctamente y producir un bundle que
 * no se publica. Mostrarlos separados es lo que hace visible esa distincion.
 */
@Component({
  selector: 'okf-verdict-badge',
  standalone: true,
  template: `<span class="badge" [class]="cls()">{{ label() }}</span>`,
})
export class VerdictBadgeComponent {
  readonly verdict = input.required<ResultClass>();

  readonly label = computed(() => {
    switch (this.verdict()) {
      case 'valid': return 'Valido';
      case 'valid_with_warnings': return 'Valido con advertencias';
      case 'invalid': return 'Invalido';
    }
  });

  readonly cls = computed(() => {
    switch (this.verdict()) {
      case 'valid': return 'badge--ok';
      case 'valid_with_warnings': return 'badge--warn';
      case 'invalid': return 'badge--err';
    }
  });
}
