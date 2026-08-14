// Modelos que reflejan el contrato de la API.
//
// Los nombres coinciden exactamente con el JSON del backend para no necesitar
// una capa de traduccion: cualquier divergencia se detecta al compilar con
// strictTemplates en lugar de a mitad de la demostracion.

export type JobStatus =
  | 'queued'
  | 'running'
  | 'canceling'
  | 'succeeded'
  | 'invalid'
  | 'failed'
  | 'dead'
  | 'canceled';

/** Los tres veredictos que la rubrica exige distinguir. */
export type ResultClass = 'valid' | 'valid_with_warnings' | 'invalid';

export type Severity = 'error' | 'warning' | 'info';

/**
 * Los hallazgos llevan el eje al que pertenecen. La validez de PLATAFORMA es la
 * puerta que decide la publicacion; la conformidad OKF es una medida de calidad
 * que nunca bloquea. La UI los presenta separados por ese motivo.
 */
export type Axis = 'platform' | 'okf';

export interface Finding {
  code: string;
  axis: Axis;
  severity: Severity;
  message: string;
  path?: string;
}

export interface ValidationReport {
  verdict: ResultClass;
  findings: Finding[];
  okf_score: number;
  okf_grade: string;
  rules_evaluated: number;
}

export interface JobEvent {
  id: number;
  attempt: number;
  type: string;
  detail?: Record<string, unknown>;
  created_at: string;
}

export interface Job {
  id: string;
  document_id: string;
  /** Nombre del fichero que origino el trabajo, resuelto por la API. */
  document_filename?: string;
  status: JobStatus;
  attempt: number;
  claim_count: number;
  max_attempts: number;
  cancel_requested: boolean;
  parent_job_id?: string;
  root_job_id?: string;
  result_class?: ResultClass;
  okf_score?: number;
  okf_grade?: string;
  validation_report?: ValidationReport;
  error_code?: string;
  error_message?: string;
  created_at: string;
  updated_at: string;
  started_at?: string;
  finished_at?: string;
  bundle_id?: string;
}

export interface JobView extends Job {
  events: JobEvent[];
}

export interface DocumentItem {
  id: string;
  filename: string;
  format: string;
  media_type: string;
  size_bytes: number;
  sha256: string;
  created_at: string;
}

export interface BundleFile {
  path: string;
  size_bytes: number;
  sha256: string;
  seq: number;
}

export interface Bundle {
  id: string;
  job_id: string;
  document_id: string;
  status: 'promoting' | 'published';
  unit_count: number;
  total_bytes: number;
  created_at: string;
  published_at?: string;
  /** Nombre del documento del que salio el bundle. */
  source_filename?: string;
  files?: BundleFile[];
}

export interface UploadResult {
  job_id: string;
  document_id: string;
  status: JobStatus;
  filename: string;
  format: string;
  size_bytes: number;
}

export interface Session {
  token: string;
  expires_at: string;
  user: { id: string; email: string; created_at: string };
}

export interface Limits {
  max_upload_bytes: number;
  allowed_extensions: string[];
  max_units: number;
}

export interface DownloadTicket {
  ticket: string;
  expires_at: string;
  url: string;
}

/** Forma unica de error de la API (RFC 7807 extendido). */
export interface ApiProblem {
  type: string;
  title: string;
  status: number;
  code: string;
  detail?: string;
  request_id?: string;
  errors?: { field: string; message: string }[];
}

/** Estados en los que el trabajo ya no cambia: cortan el sondeo. */
export const TERMINAL_STATUSES: readonly JobStatus[] = [
  'succeeded',
  'invalid',
  'failed',
  'dead',
  'canceled',
];

export function isTerminal(s: JobStatus): boolean {
  return TERMINAL_STATUSES.includes(s);
}

export function isRetryable(s: JobStatus): boolean {
  return s === 'invalid' || s === 'failed' || s === 'dead' || s === 'canceled';
}
