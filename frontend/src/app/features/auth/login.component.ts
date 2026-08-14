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
    <div class="wrap">
      <div class="card">
        <h1>{{ mode() === 'login' ? 'Acceder' : 'Crear cuenta' }}</h1>
        <p class="muted small">
          Plataforma multiusuario: cada cuenta ve unicamente sus propios
          documentos, trabajos y bundles.
        </p>

        <form (ngSubmit)="submit()">
          <div class="field">
            <label for="email">Correo</label>
            <input
              id="email"
              type="email"
              name="email"
              autocomplete="username"
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

        <p class="muted small hint">
          Usuarios de prueba creados por el arranque:
          <code>ana&#64;demo.local</code> y <code>beto&#64;demo.local</code>,
          contrasena <code>Demo12345</code>. Tener dos permite comprobar que
          ninguno accede a los recursos del otro.
        </p>
      </div>
    </div>
  `,
  styles: `
    .wrap { max-width: 420px; margin: 3rem auto; }
    .field { margin-bottom: 0.9rem; }
    .full { width: 100%; margin-top: 0.5rem; }
    .hint { margin-top: 1.25rem; border-top: 1px solid var(--border); padding-top: 0.9rem; }
    .alert { margin: 0.75rem 0; }
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
