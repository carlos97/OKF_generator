import { Component, computed, inject } from '@angular/core';
import { RouterLink, RouterLinkActive, RouterOutlet } from '@angular/router';

import { ActivityService } from './core/activity.service';
import { AuthService } from './core/auth.service';
import { IconComponent } from './shared/icon.component';
import { StatusBadgeComponent } from './shared/status-badge.component';

/**
 * Armazon de la aplicacion.
 *
 * Reproduce la composicion del documento de identidad (seccion 06): barra
 * lateral fija en escritorio, contenido principal y una barra persistente
 * abajo. En movil la lateral se convierte en navegacion inferior, como indica el
 * apartado responsive.
 *
 * Tres decisiones que no son cosmeticas:
 *
 *   - La lista "Tus trabajos" de la lateral es el equivalente al bloque de
 *     colecciones del documento, y son ENLACES REALES a /jobs/:id. Se prefirio
 *     eso a inventar secciones de menu vacias: cada entrada de la navegacion
 *     lleva a una ruta que existe, asi que el estado activo significa algo.
 *   - La barra de actividad es el equivalente del reproductor persistente.
 *     Muestra la conversion en curso desde cualquier pantalla, que es
 *     precisamente lo que esta aplicacion tiene que dejar ver: el trabajo
 *     continua en un worker aunque el usuario navegue a otro bundle.
 *   - En movil esa barra solo aparece cuando hay algo en vuelo. Con la
 *     navegacion inferior siempre presente, mantener las dos ocuparia mas de
 *     100 px de alto permanentes en una pantalla de telefono, y el documento
 *     pide justo lo contrario: reducir el hero para priorizar el contenido.
 */
@Component({
  selector: 'okf-root',
  standalone: true,
  imports: [
    RouterOutlet,
    RouterLink,
    RouterLinkActive,
    IconComponent,
    StatusBadgeComponent,
  ],
  template: `
    @if (auth.isAuthenticated()) {
      <div class="shell">
        <!-- ------------------------------------------------ lateral -------- -->
        <aside class="sidebar">
          <a routerLink="/" class="brand">
            <span class="mark" aria-hidden="true"></span>
            <span class="wordmark">OKF</span>
          </a>

          <a routerLink="/" fragment="convertir" class="btn-primary link-btn cta">
            <okf-icon name="upload" />
            Convertir documento
          </a>

          <nav class="nav" aria-label="Navegacion principal">
            <a
              routerLink="/"
              routerLinkActive="nav-item--on"
              [routerLinkActiveOptions]="{ exact: true }"
              class="nav-item"
            >
              <okf-icon name="home" />
              Panel
            </a>
            <a routerLink="/" fragment="trabajos" class="nav-item">
              <okf-icon name="stack" />
              Trabajos
              @if (activity.pending() > 0) {
                <span class="count">{{ activity.pending() }}</span>
              }
            </a>
          </nav>

          <div class="recent">
            <p class="eyebrow">Tus trabajos</p>
            @if (recent().length === 0) {
              <p class="meta empty">Todavia no hay ninguno.</p>
            } @else {
              <ul>
                @for (job of recent(); track job.id) {
                  <li>
                    <a
                      [routerLink]="['/jobs', job.id]"
                      routerLinkActive="recent-item--on"
                      class="recent-item"
                      [title]="job.document_filename ?? job.id"
                    >
                      <span class="dot" [class]="dotClass(job.status)" aria-hidden="true"></span>
                      <span class="name">{{ job.document_filename || job.id }}</span>
                    </a>
                  </li>
                }
              </ul>
            }
          </div>

          <div class="session">
            <span class="meta email" [title]="auth.email() ?? ''">{{ auth.email() }}</span>
            <button type="button" class="btn-ghost btn--sm out" (click)="auth.logout()">
              <okf-icon name="logout" />
              Salir
            </button>
          </div>
        </aside>

        <!-- --------------------------------------- cabecera en movil ------- -->
        <header class="topbar">
          <a routerLink="/" class="brand">
            <span class="mark" aria-hidden="true"></span>
            <span class="wordmark">OKF</span>
          </a>
          <button
            type="button"
            class="btn-ghost btn--sm"
            (click)="auth.logout()"
            aria-label="Cerrar sesion"
          >
            <okf-icon name="logout" />
          </button>
        </header>

        <main class="content">
          <router-outlet />
          <footer class="footer">
            ISIS4426 Desarrollo de Soluciones Cloud · Proyecto de nivelacion
          </footer>
        </main>

        <!-- ------------------------------- barra de actividad persistente -- -->
        <div
          class="activity"
          [class.activity--idle]="!activity.inFlight()"
          role="status"
          aria-live="polite"
        >
          <div class="activity-inner">
            <span class="wave" aria-hidden="true">
              <okf-icon name="wave" />
            </span>

            @if (activity.current(); as job) {
              <div class="track">
                <a [routerLink]="['/jobs', job.id]" class="track-name">
                  {{ job.document_filename || job.id }}
                </a>
                <span class="meta">
                  intento {{ job.attempt }} de {{ job.max_attempts }}
                  @if (activity.pending() > 1) {
                    · {{ activity.pending() }} sin terminar
                  }
                </span>
              </div>

              <!-- La barra solo existe mientras hay algo en vuelo. Un trabajo
                   terminado con la barra al 100% se lee como "en marcha", y
                   ademas una franja verde de ese tamano se come el 10% de
                   acento que la regla 60/30/10 reserva. -->
              @if (activity.inFlight()) {
                <div class="progress">
                  <div class="bar bar--indeterminate"><div class="bar-fill"></div></div>
                </div>
              } @else {
                <span class="filler"></span>
              }

              <okf-status-badge [status]="job.status" />

              @if (job.bundle_id) {
                <a
                  [routerLink]="['/bundles', job.bundle_id]"
                  class="btn-secondary btn--sm link-btn"
                >
                  Abrir bundle
                </a>
              }
            } @else {
              <span class="meta">
                {{ activity.loaded() ? 'Sin conversiones todavia' : 'Cargando actividad…' }}
              </span>
            }
          </div>
        </div>

        <!-- ------------------------------- navegacion inferior en movil ---- -->
        <nav class="bottom-nav" aria-label="Navegacion principal">
          <a
            routerLink="/"
            routerLinkActive="bn-item--on"
            [routerLinkActiveOptions]="{ exact: true }"
            class="bn-item"
          >
            <okf-icon name="home" />
            Panel
          </a>
          <a routerLink="/" fragment="convertir" class="bn-item">
            <okf-icon name="upload" />
            Convertir
          </a>
          <a routerLink="/" fragment="trabajos" class="bn-item">
            <okf-icon name="stack" />
            Trabajos
            @if (activity.pending() > 0) {
              <span class="count">{{ activity.pending() }}</span>
            }
          </a>
        </nav>
      </div>
    } @else {
      <!-- Sin sesion no hay armazon: la pantalla de acceso ocupa el ancho
           completo y no ensena una navegacion que no se puede usar. -->
      <main class="bare">
        <router-outlet />
      </main>
    }
  `,
  styles: `
    .shell {
      display: grid;
      grid-template-columns: 264px minmax(0, 1fr);
      max-width: var(--container);
      margin: 0 auto;
      min-height: 100vh;
    }

    /* --- lateral ---------------------------------------------------------- */

    .sidebar {
      position: sticky;
      top: 0;
      /* Menos el alto de la barra persistente: con 100vh, sus ultimos pixeles
         quedan DEBAJO de la barra y el bloque de sesion -el correo y el boton
         de salir- se vuelve inalcanzable. */
      height: calc(100vh - var(--activity-height));
      display: flex;
      flex-direction: column;
      gap: 1.15rem;
      padding: 1.35rem 1rem;
      border-right: 1px solid var(--line);
      background: var(--surface);
      overflow-y: auto;
    }

    .brand { display: flex; align-items: center; gap: 0.6rem; text-decoration: none; color: var(--text); }
    /* Marca minima: un cuadrado con el acento. No se importa ninguna forma de
       terceros; el logotipo es una figura propia. */
    .mark {
      width: 26px; height: 26px; border-radius: 8px; flex: none;
      background: linear-gradient(140deg, var(--signal), #2bb56d);
    }
    .wordmark { font-weight: 800; letter-spacing: 0.06em; font-size: 1.05rem; }

    .cta { width: 100%; }

    .nav { display: flex; flex-direction: column; gap: 0.15rem; }
    .nav-item {
      display: flex; align-items: center; gap: 0.65rem;
      padding: 0.55rem 0.6rem; border-radius: var(--radius-sm);
      color: var(--muted); text-decoration: none; font-weight: 600;
      transition: background var(--t-micro) var(--ease), color var(--t-micro) var(--ease);
    }
    .nav-item:hover { background: var(--elevated); color: var(--text); }
    /* Estado activo: acento MAS barra lateral. El color por si solo no puede ser
       el unico indicador (documento, seccion 05). */
    .nav-item--on {
      background: var(--signal-tint);
      color: var(--signal);
      box-shadow: inset 3px 0 0 var(--signal);
    }

    .count {
      margin-left: auto; min-width: 20px; text-align: center;
      font-size: 0.7rem; font-weight: 700; padding: 0.05rem 0.35rem;
      border-radius: 999px; background: var(--elevated); color: var(--text);
    }

    /* --- trabajos recientes ----------------------------------------------- */

    .recent { flex: 1; min-height: 0; overflow-y: auto; }
    .recent ul { list-style: none; margin: 0.4rem 0 0; padding: 0; display: flex; flex-direction: column; gap: 0.1rem; }
    .recent-item {
      display: flex; align-items: center; gap: 0.5rem;
      padding: 0.35rem 0.6rem; border-radius: var(--radius-sm);
      text-decoration: none; color: var(--muted); font-size: 0.85rem;
      transition: background var(--t-micro) var(--ease), color var(--t-micro) var(--ease);
    }
    .recent-item:hover { background: var(--elevated); color: var(--text); }
    .recent-item--on { background: var(--elevated); color: var(--text); font-weight: 600; }
    .recent-item .name { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .empty { margin: 0.4rem 0 0; }

    .dot { width: 7px; height: 7px; border-radius: 50%; flex: none; background: var(--muted); }
    .dot--live { background: var(--signal); animation: pulse-dot 1.4s ease-in-out infinite; }
    .dot--ok { background: var(--signal); }
    .dot--err { background: var(--err); }
    .dot--warn { background: var(--warn); }

    /* --- sesion ----------------------------------------------------------- */

    .session {
      display: flex; align-items: center; justify-content: space-between; gap: 0.5rem;
      border-top: 1px solid var(--line); padding-top: 0.9rem;
    }
    .email { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
    .out { display: inline-flex; align-items: center; gap: 0.35rem; }

    /* --- contenido -------------------------------------------------------- */

    .topbar { display: none; }

    .content {
      padding: var(--gutter);
      /* Hueco para la barra persistente, o el ultimo elemento quedaria debajo. */
      padding-bottom: calc(var(--activity-height) + var(--gutter));
      min-width: 0;
    }

    .bare { padding: var(--gutter); }

    .footer {
      margin-top: 2.5rem;
      padding-top: 1rem;
      border-top: 1px solid var(--line);
      font-size: var(--fs-meta);
      color: var(--muted);
    }

    /* --- barra de actividad ----------------------------------------------- */

    .activity {
      position: fixed;
      left: 0; right: 0; bottom: 0;
      height: var(--activity-height);
      background: rgba(21, 25, 28, 0.92);
      backdrop-filter: blur(10px);
      border-top: 1px solid var(--line);
      z-index: 20;
    }
    .activity-inner {
      max-width: var(--container);
      height: 100%;
      margin: 0 auto;
      padding: 0 var(--gutter);
      display: flex; align-items: center; gap: 0.9rem;
    }
    .wave { color: var(--signal); display: inline-flex; }
    .activity--idle .wave { color: var(--muted); }

    .track { min-width: 0; display: flex; flex-direction: column; }
    .track-name {
      font-weight: 600; color: var(--text); text-decoration: none;
      overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
    .track-name:hover { color: var(--signal); }
    .progress { flex: 1; min-width: 60px; max-width: 320px; }
    /* Empuja el estado y las acciones a la derecha cuando no hay barra. */
    .filler { flex: 1; }

    /* --- navegacion inferior ---------------------------------------------- */

    .bottom-nav { display: none; }

    /* --- responsive -------------------------------------------------------
       Un solo punto de ruptura, 900 px: por debajo, la lateral desaparece, la
       cabecera aparece y la navegacion pasa abajo. */
    @media (max-width: 900px) {
      .shell { grid-template-columns: minmax(0, 1fr); }
      .sidebar { display: none; }

      .topbar {
        display: flex; align-items: center; justify-content: space-between;
        gap: 1rem; padding: 0.7rem var(--gutter);
        background: var(--surface); border-bottom: 1px solid var(--line);
        position: sticky; top: 0; z-index: 15;
      }

      .content { padding-bottom: calc(56px + var(--gutter)); }

      /* En movil la barra se apoya sobre la navegacion inferior y solo aparece
         cuando hay trabajo en vuelo. */
      .activity { bottom: 56px; height: 56px; }
      .activity--idle { display: none; }
      .activity-inner { gap: 0.6rem; }
      .progress { display: none; }

      .bottom-nav {
        position: fixed; left: 0; right: 0; bottom: 0; z-index: 25;
        display: grid; grid-template-columns: repeat(3, 1fr);
        height: 56px;
        background: var(--surface);
        border-top: 1px solid var(--line);
      }
      .bn-item {
        display: flex; flex-direction: column; align-items: center; justify-content: center;
        gap: 0.15rem; text-decoration: none; color: var(--muted);
        font-size: 0.7rem; font-weight: 600; position: relative;
      }
      /* Igual que en la lateral: color mas barra superior, nunca solo color. */
      .bn-item--on { color: var(--signal); box-shadow: inset 0 2px 0 var(--signal); }
      .bn-item .count { position: absolute; top: 4px; right: 22%; margin: 0; }
    }
  `,
})
export class AppComponent {
  readonly auth = inject(AuthService);
  readonly activity = inject(ActivityService);

  /** Seis entradas: las que caben en la lateral sin competir con el contenido. */
  readonly recent = computed(() => this.activity.jobs().slice(0, 6));

  /**
   * Color del punto de cada trabajo reciente. Duplica a proposito la
   * clasificacion del distintivo de estado en forma de punto, porque en la
   * lateral no cabe la etiqueta completa; el nombre accesible del enlace sigue
   * siendo el del fichero y el estado se lee entero en el panel.
   */
  dotClass(status: string): string {
    switch (status) {
      case 'running':
      case 'queued':
        return 'dot--live';
      case 'succeeded':
        return 'dot--ok';
      case 'invalid':
      case 'failed':
      case 'dead':
        return 'dot--err';
      case 'canceling':
      case 'canceled':
        return 'dot--warn';
      default:
        return '';
    }
  }
}
