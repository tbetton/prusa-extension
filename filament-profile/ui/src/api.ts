// The module's own routes, and the Materials module's tools, reached with the
// session cookie the browser holds.

/** The address the host serves this module's routes under. */
const BASE = (() => {
  const match = /^(.*\/api\/v1\/modules\/[^/]+)\/ui\//u.exec(import.meta.url);

  return match?.[1] ?? '/api/v1/modules/com.github.aux.tbetton.filament-profile';
})();

/** The Materials extension answers which spool is on which tool. */
const MATERIALS = BASE.replace(/\/modules\/[^/]+$/u, '/modules/com.github.aux.prusa3d.materials');

export class ProfileError extends Error {
  constructor(
    readonly status: number,
    readonly code: string | undefined,
    readonly detail?: string,
  ) {
    super(code ?? String(status));
  }
}

const refusal = async (response: Response): Promise<ProfileError> => {
  try {
    const problem = (await response.json()) as { code?: unknown; detail?: unknown } | null;
    return new ProfileError(
      response.status,
      typeof problem?.code === 'string' ? problem.code : undefined,
      typeof problem?.detail === 'string' ? problem.detail : undefined,
    );
  } catch {
    return new ProfileError(response.status, undefined);
  }
};

const send = async (
  base: string,
  method: string,
  route: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<Response> => {
  const init: RequestInit = { method, credentials: 'same-origin', signal: signal ?? null };
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' };
    init.body = JSON.stringify(body);
  }

  const response = await fetch(`${base}${route}`, init);
  if (!response.ok) throw await refusal(response);

  return response;
};

const json = async <T>(
  base: string,
  method: string,
  route: string,
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> => (await send(base, method, route, body, signal)).json() as Promise<T>;

/** What the Materials extension reports for one tool. */
export interface Tool {
  id: string;
  active: boolean;
  assignment: {
    brand_name: string;
    material_name: string;
    type: string;
    color_rgba: string;
  } | null;
  reported: { type: string; abbreviation: string; color_rgba: string } | null;
  nozzle: { diameter_mm: number; high_flow: boolean; hardened: boolean };
}

export const listTools = async (deviceId: string, signal?: AbortSignal): Promise<Tool[]> =>
  (
    await json<{ tools: Tool[] }>(
      MATERIALS,
      'GET',
      `/printers/${encodeURIComponent(deviceId)}/tools`,
      undefined,
      signal,
    )
  ).tools;

export interface Status {
  ready: boolean;
  refreshing: boolean;
  version?: string;
  presets: number;
  updated_at?: string;
  error?: string;
}

export interface Candidate {
  preset: string;
  score?: number;
  variants: string[];
}

export interface Request {
  name: string;
  vendor?: string;
  base: string;
  variant?: string;
  color_rgba?: string;
}

export const getCatalogue = (signal?: AbortSignal): Promise<Status> =>
  json<Status>(BASE, 'GET', '/catalogue', undefined, signal);

export const refreshCatalogue = (): Promise<Status> =>
  json<Status>(BASE, 'POST', '/catalogue/refresh');

export const matchFilament = async (
  brand: string,
  name: string,
  type: string,
  signal?: AbortSignal,
): Promise<Candidate[]> => {
  const query = new URLSearchParams({ brand, name, type });

  return (
    await json<{ matches: Candidate[] }>(BASE, 'GET', `/match?${query.toString()}`, undefined, signal)
  ).matches;
};

export const searchPresets = async (q: string, signal?: AbortSignal): Promise<Candidate[]> =>
  (
    await json<{ presets: Candidate[] }>(
      BASE,
      'GET',
      `/presets?${new URLSearchParams({ q }).toString()}`,
      undefined,
      signal,
    )
  ).presets;

/** The Config Bundle text for these filaments. */
export const writeBundle = async (filaments: Request[]): Promise<string> =>
  (await send(BASE, 'POST', '/bundle', { filaments })).text();
