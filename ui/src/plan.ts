// The printer's expected activity: how long each job lasts and when it should
// start and end, worked out in the browser from what the queue already holds.

import type { DeviceJobs } from '@prusa/connect-ui/data';

import type { Job, PastPrint } from './api';

/** Time left between two prints: someone has to clear the plate and confirm it. */
export const CHANGEOVER_MS = 10 * 60_000;

/**
 * The print time PrusaSlicer writes at the end of the file name, such as
 * `_2h47m.bgcode`, `_46m.gcode` or `_1d2h3m.bgcode`, in milliseconds.
 */
export const slicerEstimate = (name: string): number | undefined => {
  const base = name.replace(/\+/gu, ' ');
  const match = /_(?:(\d+)d)?(?:(\d+)h)?(?:(\d+)m)?(?:(\d+)s)?\.(?:bgcode|gcode|bgc|gco|bbf)$/iu.exec(
    base,
  );
  if (!match || !match.slice(1, 5).some((part) => part !== undefined)) return undefined;
  const [d, h, m, s] = match.slice(1, 5).map((part) => Number(part ?? 0));
  const ms = (((d ?? 0) * 24 + (h ?? 0)) * 60 + (m ?? 0)) * 60_000 + (s ?? 0) * 1000;
  return ms > 0 ? ms : undefined;
};

/**
 * How much longer prints really take than PrusaSlicer says on this printer:
 * the median of real / estimated over the last finished prints, kept between
 * 0.8 and 1.6, or 1 with fewer than three to compare.
 */
export const correction = (history: PastPrint[]): number => {
  const ratios: number[] = [];
  for (const print of history) {
    if (print.outcome !== 'finished' || !print.started || !print.ended) continue;
    const estimate = slicerEstimate(print.display_name || print.path);
    const real = new Date(print.ended).getTime() - new Date(print.started).getTime();
    if (estimate && real > 0) ratios.push(real / estimate);
  }
  if (ratios.length < 3) return 1;
  ratios.sort((a, b) => a - b);
  const mid = Math.floor(ratios.length / 2);
  const median =
    ratios.length % 2 === 1 ? (ratios[mid] ?? 1) : ((ratios[mid - 1] ?? 1) + (ratios[mid] ?? 1)) / 2;
  return Math.min(1.6, Math.max(0.8, median));
};

export type PlanJob = Omit<Job, 'added_at' | 'added_by'>;
export type CurrentPrint = NonNullable<DeviceJobs['current']>;

/** Merge the printer's current job with the matching active queue entry. */
export const includeCurrentPrint = (
  running: Job[],
  current: CurrentPrint | undefined,
  fallbackName: string,
): PlanJob[] => {
  if (current === undefined) return running;
  const matches = (job: Job): boolean => {
    if (job.print_id) return job.print_id === current.id;
    const printerPath = job.printer_path ?? (job.storage === '' ? job.path : '');
    const started = Date.parse(job.started_at ?? '');
    return printerPath !== '' && printerPath === current.path && Date.parse(current.started) >= started;
  };
  const tracked = running.find(matches);
  // Older queue entries can match by path while the runtime event is still arriving.
  const path = current.path;
  const name = current.display_name || path.split('/').at(-1) || fallbackName;
  return [
    {
      ...(tracked ?? { id: `current:${current.id}`, device_id: current.device_id, storage: '', path, name }),
      status: 'printing',
      started_at: current.started,
    },
    ...running.filter((job) => job !== tracked),
  ];
};

export interface Slot {
  job: PlanJob;
  /** Expected start and end, in ms since the epoch. */
  start: number;
  end: number;
  /** The duration used, already corrected; undefined when the name gives none. */
  duration?: number | undefined;
  /** The slicer's own figure, before correction. */
  slicer?: number | undefined;
  /** True for the print on the printer now: its end comes from the printer. */
  running: boolean;
  progress?: number | undefined;
}

/**
 * Lays the active jobs out in time, in the order given. The running print
 * ends when the printer says; each next one starts after a changeover, or at
 * its « not before » time if that is later. A job whose duration is unknown
 * counts for nothing and is marked as such.
 */
export const schedule = (
  jobs: PlanJob[],
  now: number,
  factor: number,
  current?: { queueId: string; remainingMs: number | undefined; progress: number },
): Slot[] => {
  const slots: Slot[] = [];
  let cursor = now;
  let first = true;
  for (const job of jobs) {
    const slicer = slicerEstimate(job.name) ?? slicerEstimate(job.path);
    const running = job.status === 'printing' || job.status === 'starting';
    if (running) {
      const live = current?.queueId === job.id ? current : undefined;
      const remainingMs = live?.remainingMs;
      const reportedStart = Date.parse(job.started_at ?? '');
      const started = Number.isFinite(reportedStart) ? Math.min(reportedStart, now) : now;
      const duration =
        remainingMs !== undefined ? now - started + remainingMs : slicer ? slicer * factor : undefined;
      const end = remainingMs !== undefined ? now + remainingMs : started + (duration ?? 0);
      slots.push({ job, start: started, end: Math.max(end, now), duration, slicer, running, progress: live?.progress });
      cursor = Math.max(cursor, end, now);
      first = false;
      continue;
    }
    const duration = slicer ? slicer * factor : undefined;
    let start = first ? cursor : cursor + CHANGEOVER_MS;
    if (job.not_before) start = Math.max(start, new Date(job.not_before).getTime());
    const end = start + (duration ?? 0);
    slots.push({ job, start, end, duration, slicer, running });
    cursor = end;
    first = false;
  }
  return slots;
};
