import { formatting, SOURCE_LOCALE, type Translate } from '@prusa/connect-ui/i18n';

import { QueueError } from './api';

// Numbers, dates and durations are written once for the language of the page,
// and the formatters are kept, because building one is slow.

const numbers = new Map<string, Intl.NumberFormat>();
const dates = new Map<string, Intl.DateTimeFormat>();
const durationList = new Intl.ListFormat(SOURCE_LOCALE, { type: 'unit' });

/** A number in the page's language. */
export const formatNumber = (value: number, options?: Intl.NumberFormatOptions): string => {
  const key = JSON.stringify(options ?? {});
  let formatter = numbers.get(key);
  if (!formatter) {
    formatter = new Intl.NumberFormat(SOURCE_LOCALE, options);
    numbers.set(key, formatter);
  }
  return formatter.format(value);
};

/** A date or time in the page's language, following the 12 or 24 hour clock the person chose. */
export const formatDate = (value: string | number, options: Intl.DateTimeFormatOptions): string => {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return String(value);

  const settings: Intl.DateTimeFormatOptions = options.hour === undefined
    ? options
    : { ...options, hourCycle: formatting.clock === '12-hour' ? 'h12' : 'h23' };
  const key = JSON.stringify(settings);
  let formatter = dates.get(key);
  if (!formatter) {
    formatter = new Intl.DateTimeFormat(SOURCE_LOCALE, settings);
    dates.set(key, formatter);
  }
  return formatter.format(date);
};

const durationUnit = (value: number, unit: string): string =>
  formatNumber(value, { style: 'unit', unit, unitDisplay: 'short' });

/** A duration in its two largest units, such as "2 h 5 min". */
export const formatDuration = (ms: number): string => {
  if (!Number.isFinite(ms)) return '';
  const seconds = Math.max(1, Math.round(ms / 1000));
  if (seconds < 60) return durationUnit(seconds, 'second');
  const minutes = Math.floor(seconds / 60);
  if (minutes < 60) return durationUnit(minutes, 'minute');

  return durationList.format([
    durationUnit(Math.floor(minutes / 60), 'hour'),
    durationUnit(minutes % 60, 'minute'),
  ]);
};

/** A time of day, with the weekday when it is not today. */
export const formatClock = (ms: number, now: number): string => {
  const sameDay = new Date(now).toDateString() === new Date(ms).toDateString();
  return formatDate(ms, {
    ...(sameDay ? {} : { weekday: 'short' }),
    hour: 'numeric',
    minute: '2-digit',
  });
};

/** The sentence for a failed request: the text of its fault code, or the HTTP status. */
export const queueErrorText = (error: unknown, t: Translate): string => {
  if (error instanceof QueueError) {
    if (error.code) {
      const code = error.code.slice(error.code.lastIndexOf('/') + 1);
      if (code === 'READY_UNAVAILABLE' && error.detail) return error.detail;
      const key = `print-queue.fault.${code}`;
      const message = t(key);
      if (message !== key) return message;
    }
    return t('print-queue.fault.http', { status: String(error.status) });
  }
  return t('print-queue.fault.unknown');
};
