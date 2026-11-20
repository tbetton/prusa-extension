import { parseFileRef } from '@prusa/connect-ui/slots';
import { toast } from '@prusa/connect-ui/primitives';

import { addJob } from './api';
import { heldHost } from './host';
import { queueErrorText } from './i18n';

// The host calls this with the file's reference, `<storage>:<path>`, when a
// person picks "Add to the print queue" on a file of the server's storage.
// With one printer the file goes to its queue; with several, the person picks
// one by its number.
export default async (fileRef: string): Promise<void> => {
  const host = heldHost();
  const t = host.t;
  const file = parseFileRef(fileRef);
  if (file === undefined) {
    throw new Error(t('print-queue.fileAction.absent'));
  }

  const candidates = host.fleet.devices.filter(
    (device) => device.kind === 'printer' || device.kind === '',
  );
  if (candidates.length === 0) {
    throw new Error(t('print-queue.fileAction.noPrinter'));
  }

  let chosen = candidates[0];
  if (candidates.length > 1) {
    const list = candidates.map((device, i) => `${i + 1}. ${device.name}`).join('\n');
    const answer = window.prompt(`${t('print-queue.fileAction.pick')}\n${list}`, '1');
    if (answer === null) return;
    const index = Number.parseInt(answer, 10) - 1;
    if (!(index >= 0 && index < candidates.length)) {
      throw new Error(t('print-queue.fileAction.badPick'));
    }
    chosen = candidates[index];
  }

  if (chosen === undefined) throw new Error(t('print-queue.fileAction.badPick'));

  try {
    const job = await addJob(chosen.id, file.storage, file.path);
    toast.success(t('print-queue.fileAction.addedToPrinter', { name: job.name, printer: chosen.name }));
  } catch (error) {
    throw new Error(queueErrorText(error, t));
  }
};
