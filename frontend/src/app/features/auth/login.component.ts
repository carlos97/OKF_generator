import { HttpErrorResponse } from '@angular/common/http';
import { Component, inject, signal } from '@angular/core';
import { FormsModule } from '@angular/forms';
import { ActivatedRoute, Router } from '@angular/router';

import { AuthService } from '../../core/auth.service';
import { ApiProblem } from '../../core/models';

@Component({
  selector: 'okf-login',
  standalone: true,
  imports: [FormsModule],
  template: `
    <div class="hero">
      <!-- Columna de marca: el unico titular de campana de la aplicacion. El
           documento reserva el nivel display a la entrada, y esta es la unica
           pantalla que se ve sin sesion. -->
      <section class="pitch">
        <div class="brand">
          <span class="mark" aria-hidden="true"></span>
          <span class="wordmark">OKF</span>
        </div>

        <h1 class="display">
          Convierta documentos en <span class="hl">bundles de conocimiento</span>
        </h1>

        <p class="lead">
          Suba un fichero y la conversion continua en segundo plano: la respuesta
          es inmediata y el trabajo lo ejecuta un worker aparte, asi que puede
          cerrar la pagina sin interrumpirlo.
        </p>

        <ul class="points">
          <li>Segmentacion en unidades logicas y validacion del resultado.</li>
          <li>Cada cuenta ve unicamente sus documentos, trabajos y bundles.</li>
          <li>Traza auditable de cada intento, reintento y entrega duplicada.</li>
        </ul>
      </section>

      <!-- Columna de acceso -->
      <section class="card access enter">
        <p class="eyebrow">{{ mode() === 'login' ? 'Acceso' : 'Nueva cuenta' }}</p>
        <h2 class="access-title">
          {{ mode() === 'login' ? 'Entre a su panel' : 'Cree su cuenta' }}
        </h2>

        <form (ngSubmit)="submit()">
          <div class="field">
            <label for="email">Correo</label>
            <input
              id="email"
              type="email"
              name="email"
              autocomplete="username"
              placeholder="ana&#64;demo.local"
              required
              [(ngModel)]="email"
            />
          </div>

          <div class="field">
            <label for="password">Contrasena</label>
            <input
              id="password"
              type="password"
              name="password"
              [autocomplete]="mode() === 'login' ? 'current-password' : 'new-password'"
              placeholder="Minimo 8 caracteres"
              required
              minlength="8"
              [(ngModel)]="password"
            />
          </div>

          @if (error()) {
            <div class="alert alert--err">{{ error() }}</div>
          }

          <button
            type="submit"
            class="btn-primary full"
            [disabled]="busy() || !email || !password"
          >
            @if (busy()) {
              <span class="spinner"></span>
            }
            {{ mode() === 'login' ? 'Entrar' : 'Registrarse' }}
          </button>
        </form>

        <button type="button" class="btn-ghost full" (click)="toggle()">
          {{ mode() === 'login' ? 'No tengo cuenta' : 'Ya tengo cuenta' }}
        </button>

        <p class="meta hint">
          Usuarios de prueba creados por el arranque:
          <code>ana&#64;demo.local</code> y <code>beto&#64;demo.local</code>,
          contrasena <code>Demo12345</code>. Tener dos permite comprobar que
          ninguno accede a los recursos del otro.
        </p>
      </section>
    </div>
  `,
  styles: `
    .hero {
      display: grid;
      grid-template-columns: minmax(0, 1.05fr) minmax(320px, 420px);
      gap: 3rem;
      align-items: center;
      max-width: 1080px;
      margin: 0 auto;
      min-height: 82vh;
    }

    .brand { display: flex; align-items: center; gap: 0.6rem; margin-bottom: 2rem; }
    .mark {
      width: 30px; height: 30px; border-radius: 9px; flex: none;
      background: linear-gradient(140deg, var(--signal), #2bb56d);
    }
    .wordmark { font-weight: 800; letter-spacing: 0.06em; font-size: 1.15rem; }

    /* El acento sobre una palabra del titular, no sobre el titular entero: es
       el 10% de la regla 60/30/10, no un segundo fondo. */
    .hl { color: var(--signal); }

    .lead { color: var(--muted); font-size: 1.02rem; max-width: 46ch; }

    .points { list-style: none; padding: 0; margin: 1.75rem 0 0; display: grid; gap: 0.6rem; }
    .points li {
      position: relative;
      padding-left: 1.5rem;
      color: var(--muted);
      font-size: 0.9rem;
    }
    .points li::before {
      content: "";
      position: absolute;
      left: 0; top: 0.55em;
      width: 7px; height: 7px; border-radius: 50%;
      background: var(--signal);
    }

    .access { padding: 1.75rem; box-shadow: var(--shadow); }
    .access-title { margin: 0.15rem 0 1.25rem; font-size: var(--fs-h2); }

    .field { margin-bottom: 0.9rem; }
    .full { width: 100%; margin-top: 0.6rem; }
    .alert { margin: 0.85rem 0; }
    .hint {
      margin-top: 1.35rem;
      border-top: 1px solid var(--line);
      padding-top: 0.95rem;
      line-height: 1.6;
    }

    /* Por debajo de 900 px la columna de marca se apila sobre el formulario y
       el titular baja de tamano, que es lo que pide el apartado responsive:
       reducir el hero para priorizar el contenido. */
    @media (max-width: 900px) {
      .hero { grid-template-columns: minmax(0, 1fr); gap: 2rem; min-height: 0; padding: 1rem 0; }
      .brand { margin-bottom: 1.25rem; }
      .points { display: none; }
      .lead { font-size: 0.95rem; }
    }
  `,
})
export class LoginComponent {
  private readonly auth = inject(AuthService);
  private readonly router = inject(Router);
  private readonly route = inject(ActivatedRoute);

  email = '';
  password = '';

  readonly mode = signal<'login' | 'register'>('login');
  readonly busy = signal(false);
  readonly error = signal<string | null>(null);

  toggle(): void {
    this.mode.update((m) => (m === 'login' ? 'register' : 'login'));
    this.error.set(null);
  }

  submit(): void {
    if (this.busy()) {
      return;
    }
    this.busy.set(true);
    this.error.set(null);

    const call =
      this.mode() === 'login'
        ? this.auth.login(this.email, this.password)
        : this.auth.register(this.email, this.password);

    call.subscribe({
      next: () => {
        const returnUrl = this.route.snapshot.queryParamMap.get('returnUrl') ?? '/';
        void this.router.navigateByUrl(returnUrl);
      },
      error: (err: unknown) => {
        this.busy.set(false);
        this.error.set(describeError(err));
      },
    });
  }
}

/**
 * Traduce la respuesta de error a un mensaje para el usuario.
 *
 * La API usa una forma unica (RFC 7807 extendido con `code`), asi que basta una
 * funcion para toda la aplicacion y los mensajes de campo se muestran tal cual.
 */
export function describeError(err: unknown): string {
  if (err instanceof HttpErrorResponse) {
    const p = err.error as ApiProblem | undefined;
    if (p?.errors?.length) {
      return p.errors.map((f) => `${f.field}: ${f.message}`).join(' · ');
    }
    if (p?.title) {
      return p.title;
    }
    if (err.status === 0) {
      return 'No se pudo contactar con el servidor.';
    }
    return `Error ${err.status}`;
  }
  return 'Error inesperado.';
}
