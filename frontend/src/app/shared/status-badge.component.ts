import { Component, computed, input } from '@angular/core';

import { JobStatus, ResultClass } from '../core/models';

/**
 * Etiqueta legible de cada estado.
 *
 * Vive fuera del componente porque el buscador del panel filtra por lo que el
 * usuario VE ("completado", "en cola"), no por el identificador del contrato de
 * la API. Con la tabla duplicada, escribir en el buscador el texto de un
 * distintivo no habria encontrado nada.
 */
export function statusLabel(status: JobStatus): string {
  switch (status) {
    case 'queued': return 'En cola';
    case 'running': return 'Procesando';
    case 'canceling': return 'Cancelando';
    case 'succeeded': return 'Completado';
    case 'invalid': return 'Bundle invalido';
    case 'failed': return 'Fallido';
    case 'dead': return 'Agotados los reintentos';
    case 'canceled': return 'Cancelado';
  }
}

/**
 * Distintivo del ESTADO del trabajo.
 *
 * El estado se lee por texto y por forma, no por color: la clase base pinta un
 * punto y en los estados en curso ese punto late. Es lo que exige el documento
 * de identidad (seccion 05, accesibilidad): el color nunca puede ser el unico
 * indicador de estado.
 */
@Component({
  selector: 'okf-status-badge',
  standalone: true,
  template: `<span class="badge" [class]="cls()">{{ label() }}</span>`,
})
export class StatusBadgeComponent {
  readonly status = input.required<JobStatus>();

  readonly label = computed(() => statusLabel(this.status()));

  readonly cls = computed(() => {
    switch (this.status()) {
      case 'succeeded': return 'badge--ok';
      case 'invalid': case 'failed': case 'dead': return 'badge--err';
      case 'canceling': return 'badge--warn badge--live';
      case 'canceled': return 'badge--warn';
      case 'running': return 'badge--ok badge--live';
      case 'queued': return 'badge--idle badge--live';
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
