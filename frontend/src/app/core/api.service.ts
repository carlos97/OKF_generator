import { HttpClient, HttpEvent, HttpEventType } from '@angular/common/http';
import { Injectable, inject } from '@angular/core';
import { Observable, filter, map, timer } from 'rxjs';
import { switchMap, takeWhile } from 'rxjs/operators';

import {
  Bundle,
  DocumentItem,
  DownloadTicket,
  Job,
  JobView,
  Limits,
  UploadResult,
  isTerminal,
} from './models';

interface Page<T> {
  items: T[];
}

@Injectable({ providedIn: 'root' })
export class ApiService {
  private readonly http = inject(HttpClient);

  // --- metadatos -----------------------------------------------------------

  limits(): Observable<Limits> {
    return this.http.get<Limits>('/api/v1/meta/limits');
  }

  // --- documentos ----------------------------------------------------------

  /**
   * Sube el documento y devuelve el identificador del trabajo.
   *
   * Se pide `reportProgress` porque la respuesta llega de inmediato (202) y sin
   * la barra de progreso de la SUBIDA no se apreciaria que el fichero pesado
   * tarda en viajar mientras la respuesta del servidor es instantanea. Es
   * justamente lo que hace visible la asincronia en el video.
   */
  upload(file: File): Observable<HttpEvent<UploadResult>> {
    const form = new FormData();
    form.append('file', file, file.name);
    return this.http.post<UploadResult>('/api/v1/documents', form, {
      reportProgress: true,
      observe: 'events',
    });
  }

  uploadProgress(event: HttpEvent<unknown>): number | null {
    if (event.type === HttpEventType.UploadProgress && event.total) {
      return Math.round((100 * event.loaded) / event.total);
    }
    return null;
  }

  documents(): Observable<DocumentItem[]> {
    return this.http
      .get<Page<DocumentItem>>('/api/v1/documents')
      .pipe(map((p) => p.items));
  }

  deleteDocument(id: string): Observable<void> {
    return this.http.delete<void>(`/api/v1/documents/${id}`);
  }

  // --- trabajos ------------------------------------------------------------

  jobs(): Observable<Job[]> {
    return this.http.get<Page<Job>>('/api/v1/jobs').pipe(map((p) => p.items));
  }

  job(id: string): Observable<JobView> {
    return this.http.get<JobView>(`/api/v1/jobs/${id}`);
  }

  /**
   * Sondeo del estado con RxJS.
   *
   * Se elige sondeo y NO Server-Sent Events: con varias replicas de la API
   * detras del proxy, SSE obligaria a que la replica que sostiene la conexion se
   * enterase de los eventos que produce el worker, lo que exigiria un fan-out
   * por la cola y una conexion larga por cliente. El sondeo es sin estado por
   * definicion (cualquier replica responde cualquier GET) y en pantalla se ve
   * igual de bien: los estados avanzan solos.
   *
   * `takeWhile` con `inclusive: true` deja pasar el ultimo valor terminal, para
   * que la UI muestre el resultado final antes de detenerse.
   */
  pollJob(id: string, everyMs = 1500): Observable<JobView> {
    return timer(0, everyMs).pipe(
      switchMap(() => this.job(id)),
      takeWhile((j) => !isTerminal(j.status), true),
    );
  }

  pollJobs(everyMs = 3000): Observable<Job[]> {
    return timer(0, everyMs).pipe(switchMap(() => this.jobs()));
  }

  retryJob(id: string): Observable<Job> {
    return this.http.post<Job>(`/api/v1/jobs/${id}/retry`, {});
  }

  cancelJob(id: string): Observable<{ status: string }> {
    return this.http.post<{ status: string }>(`/api/v1/jobs/${id}/cancel`, {});
  }

  /** Herramienta de demostracion de idempotencia (requiere DEV_TOOLS=true). */
  replayJob(id: string, times = 2): Observable<{ republished: number }> {
    return this.http.post<{ republished: number }>(
      `/api/v1/jobs/${id}/replay?times=${times}`,
      {},
    );
  }

  // --- bundles -------------------------------------------------------------

  bundles(): Observable<Bundle[]> {
    return this.http
      .get<Page<Bundle>>('/api/v1/bundles')
      .pipe(map((p) => p.items));
  }

  bundle(id: string): Observable<Bundle> {
    return this.http.get<Bundle>(`/api/v1/bundles/${id}`);
  }

  /** Lee un fichero del bundle como texto, para el visor. */
  bundleFile(id: string, path: string): Observable<string> {
    return this.http.get(`/api/v1/bundles/${id}/files/${path}`, {
      responseType: 'text',
    });
  }

  /**
   * Lee un asset como blob para poder mostrarlo en un <img>.
   *
   * Un <img src="/api/v1/..."> no puede enviar la cabecera Authorization, asi que
   * con el token en memoria la peticion recibiria un 401 y la imagen apareceria
   * rota justo en el segmento del video que ensena la extraccion de recursos.
   * Traerla por HttpClient (donde el interceptor si anade la cabecera) y crear una
   * URL de objeto resuelve el problema. Aqui SI es aceptable usar un blob, al
   * contrario que con el ZIP: los assets estan acotados por ASSETS_MAX_BYTES y
   * son de unos pocos MB como maximo.
   */
  bundleAsset(id: string, path: string): Observable<Blob> {
    return this.http.get(`/api/v1/bundles/${id}/files/${path}`, {
      responseType: 'blob',
    });
  }

  /**
   * Descarga el documento ORIGINAL tal y como se subio.
   *
   * Sirve para comparar la entrada con el bundle generado, que es lo que se
   * quiere hacer al revisar una conversion.
   *
   * Se usa un blob y no un ticket, al contrario que con el ZIP: el original esta
   * acotado por MAX_UPLOAD_BYTES (20 MiB), asi que cabe en memoria de la pestana
   * sin riesgo, y de este modo no hace falta emitir ni consumir un ticket para
   * algo que ya esta limitado. La API lo sirve por streaming de todas formas.
   */
  documentContent(documentId: string): Observable<Blob> {
    return this.http.get(`/api/v1/documents/${documentId}/content`, {
      responseType: 'blob',
    });
  }

  /**
   * Descarga el ZIP.
   *
   * Se pide un ticket de un solo uso y se navega a la URL resultante, en lugar
   * de traer el ZIP con fetch a un Blob. El motivo es concreto: la API sirve el
   * paquete por streaming sin materializarlo, y un Blob lo acumularia entero en
   * la memoria de la pestana, anulando esa propiedad y bloqueando el navegador
   * con bundles grandes. Ademas un <a href> no puede enviar la cabecera
   * Authorization, y el ticket resuelve exactamente ese problema.
   *
   * El ticket se pide EN EL MOMENTO DEL CLIC, nunca al cargar la pagina: con un
   * TTL corto, uno pedido antes ya habria caducado.
   */
  download(bundleId: string): Observable<DownloadTicket> {
    return this.http.post<DownloadTicket>(
      `/api/v1/bundles/${bundleId}/download-tickets`,
      {},
    );
  }

  /** Filtra los eventos de subida hasta la respuesta final. */
  static onlyResponse<T>() {
    return (source: Observable<HttpEvent<T>>) =>
      source.pipe(
        filter((e): e is HttpEvent<T> & { body: T } => e.type === HttpEventType.Response),
        map((e) => e.body as T),
      );
  }
}
