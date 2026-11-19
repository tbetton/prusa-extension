// The module's own routes, reached with the session cookie the browser holds.

/** The address the host serves this module's routes under. */
const BASE = (() => {
  const match = /^(.*\/api\/v1\/modules\/[^/]+)\/ui\//u.exec(import.meta.url);

  return match?.[1] ?? '/api/v1/modules/com.github.aux.tbetton.print-queue';
})();

export const moduleNamespace = decodeURIComponent(BASE.slice(BASE.lastIndexOf('/') + 1));

export interface Job {
  id: string;
  device_id: string;
  storage: string;
  path: string;
  name: string;
  added_by?: string;
  added_at: string;
  not_before?: string;
  status: 'queued' | 'starting' | 'printing' | 'done' | 'stopped' | 'failed';
  detail?: string;
  detail_code?: string;
  printer_path?: string;
  print_id?: string;
  started_at?: string;
  ended_at?: string;
}

export interface Queue {
  jobs: Job[];
  paused: Record<string, boolean>;
  plate_checks?: Record<string, PlateCheck>;
  problem?: string;
}

export interface PlateCheck {
  automatic: boolean;
  reference_at?: string;
  last_match_at?: string;
  status: string;
  can_configure: boolean;
}

export class QueueError extends Error {
  constructor(
    readonly status: number,
    readonly code: string | undefined,
    readonly detail?: string,
  ) {
    super(code ?? String(status));
  }
}

const refusal = async (response: Response): Promise<QueueError> => {
  try {
    const problem = (await response.json()) as { code?: unknown; detail?: unknown } | null;
    return new QueueError(
      response.status,
      typeof problem?.code === 'string' ? problem.code : undefined,
      typeof problem?.detail === 'string' ? problem.detail : undefined,
    );
  } catch {
    return new QueueError(response.status, undefined);
  }
};

const call = async <T>(
  method: string,
  route: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> => {
  const init: RequestInit = { method, credentials: 'same-origin', signal: signal ?? null };
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(body);
  }

  const response = await fetch(`${BASE}${route}`, init);
  if (!response.ok) throw await refusal(response);

  if (response.status === 204) return undefined as T;

  return (await response.json()) as T;
};

export const listJobs = (device: string | undefined, signal?: AbortSignal): Promise<Queue> =>
  call<Queue>('GET', device ? `/jobs?device=${encodeURIComponent(device)}` : '/jobs', undefined, signal);

export const addJob = (
  deviceId: string,
  storage: string,
  path: string,
  name?: string,
): Promise<Job> => call<Job>('POST', '/jobs', { device_id: deviceId, storage, path, name });

/** One entry of a printer's own drive, as the core lists it. */
export interface PrinterFile {
  name: string;
  display_name?: string;
  path: string;
  folder: boolean;
  size: number;
  modified?: string;
}

/** One folder of a printer's drive; an empty path is the drive list. */
export const listPrinterFiles = async (
  deviceId: string,
  path: string,
  refresh = false,
): Promise<{ items: PrinterFile[]; reading: boolean }> => {
  const query = new URLSearchParams({ path });
  if (refresh) query.set('refresh', 'true');
  const response = await fetch(
    `/api/v1/devices/${encodeURIComponent(deviceId)}/files?${query.toString()}`,
    { credentials: 'same-origin' },
  );
  if (!response.ok) throw await refusal(response);
  const body = (await response.json()) as { items?: PrinterFile[] | null; reading?: boolean };
  return { items: body.items ?? [], reading: body.reading === true };
};

export const removeJob = (id: string): Promise<void> => call('DELETE', `/jobs/${id}`);

export const moveJob = (id: string, direction: 'up' | 'down'): Promise<void> =>
  call('POST', `/jobs/${id}/move`, { direction });

export const scheduleJob = (id: string, at: Date | null): Promise<void> =>
  call('PUT', `/jobs/${id}/schedule`, { not_before: at === null ? null : at.toISOString() });

export const requeueJob = (id: string): Promise<void> => call('POST', `/jobs/${id}/requeue`);

export const clearFinished = (deviceId: string | undefined): Promise<void> =>
  call('POST', '/jobs/clear-finished', { device_id: deviceId ?? '' });

export const setPaused = (deviceId: string, paused: boolean): Promise<void> =>
  call('PUT', `/devices/${deviceId}/paused`, { paused });

export const markPrinterReady = (deviceId: string): Promise<void> =>
  call('POST', `/devices/${encodeURIComponent(deviceId)}/ready`, { plate_empty: true });

export const updatePlateCheck = (
  deviceId: string,
  settings: { action: 'capture_reference' } | { action: 'set_automatic'; automatic: boolean },
): Promise<void> => call('POST', `/devices/${encodeURIComponent(deviceId)}/plate-check`, settings);

/**
 * The address of a file's picture: from the server's storage, or from the
 * printer's own drive when storage is empty. The browser sends the session
 * cookie with it, as with every request of the page.
 */
export const thumbnailUrl = (
  deviceId: string,
  storage: string,
  path: string,
  size?: number,
  modified?: string,
): string => {
  const query = new URLSearchParams({ path });
  if (storage !== '') {
    return `/api/v1/storages/${encodeURIComponent(storage)}/thumbnail?${query.toString()}`;
  }
  if (size) query.set('size', String(size));
  if (modified) query.set('modified', modified);
  return `/api/v1/devices/${encodeURIComponent(deviceId)}/files/thumbnail?${query.toString()}`;
};

/** A print the printer finished, from the core's job history. */
export interface PastPrint {
  display_name?: string;
  path: string;
  started?: string;
  ended?: string;
  outcome?: string;
}

/** The printer's last finished prints, to compare real durations with estimates. */
export const listHistory = async (deviceId: string, signal?: AbortSignal): Promise<PastPrint[]> => {
  const init: RequestInit = { credentials: 'same-origin' };
  if (signal) init.signal = signal;
  const response = await fetch(
    `/api/v1/devices/${encodeURIComponent(deviceId)}/jobs/history?limit=30`,
    init,
  );
  if (!response.ok) return [];
  const body = (await response.json()) as { items?: PastPrint[] };
  return body.items ?? [];
};
