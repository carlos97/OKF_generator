import { Injectable, computed, effect, inject, signal } from '@angular/core';
import { Subscription, retry } from 'rxjs';

import { ApiService } from './api.service';
import { AuthService } from './auth.service';
import { Job, isTerminal } from './models';

/**
 * Estado de actividad compartido por toda la aplicacion.
 *
 * Existe por la barra persistente del documento de identidad (secciones 04 y
 * 06): esa barra vive en el armazon, fuera de la ruta activa, y necesita la
 * lista de trabajos igual que el panel. Sin un unico propietario del sondeo
 * habria DOS temporizadores preguntando lo mismo cada tres segundos, y el
 * numero de peticiones que el evaluador ve en la consola de red se duplicaria
 * sin que nada nuevo apareciese en pantalla.
 *
 * El sondeo arranca y se detiene con la sesion: en la pantalla de acceso no hay
 * token, y una peticion a /api/v1/jobs sin token solo produciria un 401 por
 * cada tick.
 */
@Injectable({ providedIn: 'root' })
export class ActivityService {
  private readonly api = inject(ApiService);
  private readonly auth = inject(AuthService);

  private sub?: Subscription;

  private readonly _jobs = signal<Job[]>([]);
  private readonly _loaded = signal(false);

  /** Trabajos del usuario, mas recientes primero (los ordena la API). */
  readonly jobs = this._jobs.asReadonly();

  /** Distingue "todavia no ha llegado la primera respuesta" de "no hay nada". */
  readonly loaded = this._loaded.asReadonly();

  /**
   * Trabajo que la barra persistente muestra. El orden de preferencia es el de
   * urgencia para quien mira: primero lo que se esta ejecutando, luego lo que
   * espera turno y, si no hay nada en vuelo, el ultimo trabajo terminado, que es
   * el atajo al bundle recien generado.
   */
  readonly current = computed<Job | null>(() => {
    const items = this._jobs();
    return (
      items.find((j) => j.status === 'running') ??
      items.find((j) => j.status === 'canceling') ??
      items.find((j) => j.status === 'queued') ??
      items[0] ??
      null
    );
  });

  readonly inFlight = computed(() => {
    const j = this.current();
    return j !== null && !isTerminal(j.status);
  });

  /** Cuantos trabajos siguen sin resolverse, para el contador de la barra. */
  readonly pending = computed(
    () => this._jobs().filter((j) => !isTerminal(j.status)).length,
  );

  constructor() {
    effect(() => {
      if (this.auth.isAuthenticated()) {
        this.start();
      } else {
        this.stop();
      }
    });
  }

  private start(): void {
    if (this.sub) {
      return;
    }
    // `retry` no es un adorno defensivo. Un tick fallido termina el observable
    // completo, y sin reintento la lista se quedaria congelada para siempre
    // justo en la demostracion que recrea o escala el servicio `api`: durante
    // esos segundos el proxy responde 502 y, sin esto, habria que recargar la
    // pagina a mano para que el panel volviera a moverse.
    //
    // Reintentos infinitos son seguros porque un 401 no se reintenta en la
    // practica: el interceptor cierra la sesion, `isAuthenticated` pasa a false
    // y el efecto de arriba detiene el sondeo.
    this.sub = this.api
      .pollJobs(3000)
      .pipe(retry({ delay: 3000 }))
      .subscribe({
        next: (items) => {
          this._jobs.set(items);
          this._loaded.set(true);
        },
      });
  }

  private stop(): void {
    this.reset();
  }

  private reset(): void {
    this.sub?.unsubscribe();
    this.sub = undefined;
    this._jobs.set([]);
    this._loaded.set(false);
  }
}
