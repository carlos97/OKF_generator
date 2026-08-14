import { Component, inject } from '@angular/core';
import { RouterLink, RouterOutlet } from '@angular/router';

import { AuthService } from './core/auth.service';

@Component({
  selector: 'okf-root',
  standalone: true,
  imports: [RouterOutlet, RouterLink],
  template: `
    <header class="topbar">
      <a routerLink="/" class="brand">
        <span class="logo">OKF</span>
        <span class="title">Conversion documental a bundles de conocimiento</span>
      </a>

      @if (auth.isAuthenticated()) {
        <div class="session">
          <span class="email">{{ auth.email() }}</span>
          <button type="button" class="btn-ghost" (click)="auth.logout()">
            Cerrar sesion
          </button>
        </div>
      }
    </header>

    <main class="content">
      <router-outlet />
    </main>

    <footer class="footer">
      ISIS4426 Desarrollo de Soluciones Cloud · Proyecto de nivelacion
    </footer>
  `,
  styles: `
    .topbar {
      display: flex; align-items: center; justify-content: space-between;
      gap: 1rem; padding: 0.75rem 1.5rem;
      background: var(--surface-2); border-bottom: 1px solid var(--border);
    }
    .brand { display: flex; align-items: center; gap: 0.75rem; text-decoration: none; color: inherit; }
    .logo {
      font-weight: 700; letter-spacing: 0.05em; padding: 0.25rem 0.5rem;
      border-radius: 6px; background: var(--accent); color: #fff; font-size: 0.9rem;
    }
    .title { font-size: 0.95rem; color: var(--text-2); }
    .session { display: flex; align-items: center; gap: 0.75rem; }
    .email { font-size: 0.85rem; color: var(--text-2); }
    .content { max-width: 1200px; margin: 0 auto; padding: 1.5rem; }
    .footer {
      padding: 1rem 1.5rem; text-align: center;
      font-size: 0.8rem; color: var(--text-3); border-top: 1px solid var(--border);
    }
    @media (max-width: 640px) {
      .title { display: none; }
    }
  `,
})
export class AppComponent {
  readonly auth = inject(AuthService);
}
