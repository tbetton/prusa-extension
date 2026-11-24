// The module's own routes, and the Materials module's tools, reached with the
// session cookie the browser holds.

import type { components } from '@prusa/connect';

import { heldHost } from './host';
import { toolsFromTree } from './printer-tools';

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
    material_uuid: string;
    brand_name: string;
    material_name: string;
    type: string;
    color_rgba: string;
  } | null;
  reported: { type: string; abbreviation: string; color_rgba: string } | null;
  nozzle: { diameter_mm: number; high_flow: boolean; hardened: boolean; high_temp?: boolean };
}

const listToolsFromMaterials = async (deviceId: string, signal?: AbortSignal): Promise<Tool[]> =>
  (
    await json<{ tools: Tool[] }>(
      MATERIALS,
      'GET',
      `/printers/${encodeURIComponent(deviceId)}/tools`,
      undefined,
      signal,
    )
  ).tools;

type DeviceTool = components['schemas']['DeviceTool'];

const listToolsFromPrinter = async (deviceId: string, signal?: AbortSignal): Promise<Tool[]> => {
  const host = heldHost();
  const device = await host.client.getDevice({ path: { device_id: deviceId } }, signal ?? host.signal);
  const tree: Record<string, DeviceTool | null> = device.tools?.tools ?? {};

  return toolsFromTree(tree);
};

/** Where the tools were read: the Materials extension, or the printer alone. */
export type ToolSource = 'materials' | 'printer';

/**
 * The tools of a printer. The Materials extension knows the brand, the product
 * and the colour of each spool, so it comes first. Without it (not installed,
 * stopped, or not following this printer), the printer's own report is read.
 */
export const listTools = async (
  deviceId: string,
  signal?: AbortSignal,
): Promise<{ tools: Tool[]; source: ToolSource }> => {
  try {
    return { tools: await listToolsFromMaterials(deviceId, signal), source: 'materials' };
  } catch (error) {
    // Not found, or a module that is down. A refusal of access is not hidden.
    if (!(error instanceof ProfileError) || ![404, 502, 503].includes(error.status)) throw error;

    return { tools: await listToolsFromPrinter(deviceId, signal), source: 'printer' };
  }
};

export interface Status {
  ready: boolean;
  refreshing: boolean;
  version?: string;
  presets: number;
  /** The number of presets each source gave. */
  sources?: Record<string, number>;
  /** The sources the operator chose to offer. */
  enabled?: string[];
  updated_at?: string;
  error?: string;
}

export interface Candidate {
  preset: string;
  score?: number;
  variants: string[];
  /** Where the preset comes from: prusa, polymaker or custom. */
  source?: string;
  /** The section written for the printer asked about. */
  section?: string;
  /** It does not fit that printer as written: its material settings are laid over a generic preset. */
  adapted?: boolean;
}

/** One printer preset of PrusaSlicer. */
export interface Printer {
  name: string;
  model?: string;
  variant?: string;
  extruders: number;
  nozzle_diameter?: number;
  high_flow: boolean;
}

export interface Request {
  name: string;
  vendor?: string;
  base: string;
  color_rgba?: string;
  /** The filament position in PrusaSlicer, from 1. */
  slot?: number;
  /** The maker's advised temperatures and the density. */
  properties?: Properties;
  /** Settings the person edited, by key: they win over the preset. */
  overrides?: Record<string, string>;
}

/** What the OpenPrintTag catalogue says of one material. Absent or 0 is unknown. */
export interface Properties {
  min_print_temp?: number;
  max_print_temp?: number;
  min_bed_temp?: number;
  max_bed_temp?: number;
  density?: number;
}

/** The catalogue's properties of one material, as the Materials extension serves them. */
export const getMaterial = async (uuid: string, signal?: AbortSignal): Promise<Properties> => {
  const detail = await json<{ material?: Properties; custom?: Properties }>(
    MATERIALS,
    'GET',
    `/materials/${encodeURIComponent(uuid)}`,
    undefined,
    signal,
  );

  const found = detail.material ?? detail.custom ?? {};
  const keep: Properties = {};
  // Only what the profile uses is sent on, not the whole material.
  if (found.min_print_temp) keep.min_print_temp = found.min_print_temp;
  if (found.max_print_temp) keep.max_print_temp = found.max_print_temp;
  if (found.min_bed_temp) keep.min_bed_temp = found.min_bed_temp;
  if (found.max_bed_temp) keep.max_bed_temp = found.max_bed_temp;
  if (found.density) keep.density = found.density;

  return keep;
};

export const getCatalogue = (signal?: AbortSignal): Promise<Status> =>
  json<Status>(BASE, 'GET', '/catalogue', undefined, signal);

export const refreshCatalogue = (): Promise<Status> =>
  json<Status>(BASE, 'POST', '/catalogue/refresh');

export const matchFilament = async (
  brand: string,
  name: string,
  type: string,
  printer: string,
  signal?: AbortSignal,
): Promise<Candidate[]> => {
  const query = new URLSearchParams({ brand, name, type });
  if (printer !== '') query.set('printer', printer);

  return (
    await json<{ matches: Candidate[] }>(BASE, 'GET', `/match?${query.toString()}`, undefined, signal)
  ).matches;
};

export const searchPresets = async (
  q: string,
  printer: string,
  signal?: AbortSignal,
): Promise<Candidate[]> => {
  const query = new URLSearchParams({ q });
  if (printer !== '') query.set('printer', printer);

  return (
    await json<{ presets: Candidate[] }>(BASE, 'GET', `/presets?${query.toString()}`, undefined, signal)
  ).presets;
};

/** The printer presets of PrusaSlicer whose name holds the words of the query. */
export const listPrinters = async (q: string, signal?: AbortSignal): Promise<Printer[]> =>
  (
    await json<{ printers: Printer[] }>(
      BASE,
      'GET',
      `/printers?${new URLSearchParams({ q }).toString()}`,
      undefined,
      signal,
    )
  ).printers;

/** The Config Bundle text for these filaments. */
export const writeBundle = async (printer: string, filaments: Request[]): Promise<string> =>
  (await send(BASE, 'POST', '/bundle', { printer, filaments })).text();

/** Every base preset the module offers, without its printer variants. */
export const listAllPresets = async (signal?: AbortSignal): Promise<Candidate[]> =>
  (
    await json<{ presets: Candidate[] }>(BASE, 'GET', '/presets?limit=2000&brief=1', undefined, signal)
  ).presets;

/** The settings a profile would be written with, and the section they come from. */
export interface Settings {
  settings: Record<string, string>;
  section: string;
  /** The generic preset the settings were laid over, when the preset did not fit the printer. */
  adapted?: string;
}

/** The keys a person may edit, in the order the panel shows them. */
export const EDITABLE = [
  'filament_type',
  'temperature',
  'first_layer_temperature',
  'bed_temperature',
  'first_layer_bed_temperature',
  'filament_density',
  'filament_diameter',
  'extrusion_multiplier',
  'filament_max_volumetric_speed',
  'min_fan_speed',
  'max_fan_speed',
  'bridge_fan_speed',
  'filament_retract_length',
  'filament_retract_speed',
  'filament_cost',
] as const;

export const getSettings = (
  base: string,
  printer: string,
  properties: Properties | undefined,
  signal?: AbortSignal,
): Promise<Settings> => {
  const body: Record<string, unknown> = { base, printer };
  if (properties) body.properties = properties;

  return json<Settings>(BASE, 'POST', '/settings', body, signal);
};

/** What is kept of a device's tab: the printer and, by tool, what was chosen. */
export interface SavedTool {
  /** The spool it was chosen for: a changed spool drops the choice. */
  spool: string;
  include: boolean;
  name: string;
  preset: string;
  edits: Record<string, string>;
  useProperties?: boolean;
}

export interface SavedState {
  printer?: string;
  tools?: Record<string, SavedTool>;
}

export const getState = (deviceId: string, signal?: AbortSignal): Promise<SavedState> =>
  json<SavedState>(BASE, 'GET', `/state/${encodeURIComponent(deviceId)}`, undefined, signal);

export const putState = async (deviceId: string, state: SavedState): Promise<void> => {
  await send(BASE, 'POST', `/state/${encodeURIComponent(deviceId)}`, state);
};
