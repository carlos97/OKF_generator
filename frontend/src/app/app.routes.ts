import { Routes } from '@angular/router';

import { authGuard, guestGuard } from './core/auth.guard';

// Todas las rutas privadas son perezosas: la pantalla de acceso carga sin
// arrastrar el visor de bundles ni el resto de la aplicacion.
export const routes: Routes = [
  {
    path: 'login',
    canActivate: [guestGuard],
    loadComponent: () =>
      import('./features/auth/login.component').then((m) => m.LoginComponent),
    title: 'Acceso · OKF',
  },
  {
    path: '',
    canActivate: [authGuard],
    loadComponent: () =>
      import('./features/dashboard/dashboard.component').then(
        (m) => m.DashboardComponent,
      ),
    title: 'Panel · OKF',
  },
  {
    path: 'jobs/:id',
    canActivate: [authGuard],
    loadComponent: () =>
      import('./features/jobs/job-detail.component').then(
        (m) => m.JobDetailComponent,
      ),
    title: 'Trabajo · OKF',
  },
  {
    path: 'bundles/:id',
    canActivate: [authGuard],
    loadComponent: () =>
      import('./features/bundles/bundle-viewer.component').then(
        (m) => m.BundleViewerComponent,
      ),
    title: 'Bundle · OKF',
  },
  { path: '**', redirectTo: '' },
];
