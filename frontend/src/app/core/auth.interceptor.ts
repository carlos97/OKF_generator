import { HttpErrorResponse, HttpInterceptorFn } from '@angular/common/http';
import { inject } from '@angular/core';
import { Router } from '@angular/router';
import { catchError, throwError } from 'rxjs';

import { AuthService } from './auth.service';

/**
 * Interceptor funcional (no clase): es la forma actual en Angular y no necesita
 * providers adicionales.
 *
 * Anade el Bearer a todas las llamadas a /api salvo las de autenticacion, y
 * ante un 401 limpia la sesion y devuelve al login conservando la ruta de
 * origen, para que el usuario no pierda donde estaba.
 */
export const authInterceptor: HttpInterceptorFn = (req, next) => {
  const auth = inject(AuthService);
  const router = inject(Router);

  const isAuthCall = req.url.includes('/api/v1/auth/');
  const token = auth.token();

  const authorized =
    token && !isAuthCall
      ? req.clone({ setHeaders: { Authorization: `Bearer ${token}` } })
      : req;

  return next(authorized).pipe(
    catchError((err: unknown) => {
      if (err instanceof HttpErrorResponse && err.status === 401 && !isAuthCall) {
        auth.logout(false);
        void router.navigate(['/login'], {
          queryParams: { returnUrl: router.url },
        });
      }
      return throwError(() => err);
    }),
  );
};
