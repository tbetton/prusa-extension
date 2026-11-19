import {
  formatDate,
  formatDuration as durationInSeconds,
  type Translate,
} from '@prusa/connect-ui/i18n';

import { QueueError } from './api';

export const formatDuration = (ms: number): string =>
  durationInSeconds(Math.max(1, Math.round(ms / 1000)));

export const formatClock = (ms: number, now: number): string => {
  const sameDay = new Date(now).toDateString() === new Date(ms).toDateString();
  return formatDate(ms, {
    ...(sameDay ? {} : { weekday: 'short' }),
    hour: 'numeric',
    minute: '2-digit',
  });
};

export const queueErrorText = (error: unknown, t: Translate): string => {
  if (error instanceof QueueError) {
    if (error.code) {
      const key = `print-queue.fault.${error.code.split('/').at(-1)}`;
      const message = t(key);
      if (message !== key) return message;
      const coreKey = `error.${error.code}`;
      const coreMessage = t(coreKey);
      if (coreMessage !== coreKey) return coreMessage;
    }
    return t('print-queue.fault.http', { status: error.status });
  }
  return t('print-queue.fault.unknown');
};
