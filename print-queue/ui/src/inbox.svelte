<!--
  The files PrusaSlicer left for one printer, in the folder the PrusaSlicer 2
  Connector saves its uploads in. A person adds a file to the queue or
  ignores it, and may turn on the automatic adding for the printer.

  It draws nothing for a person who may not operate the printer, and nothing
  until the first answer.
-->
<script lang="ts">
  import { onDestroy } from 'svelte';
  import { Badge, Button, toast } from '@prusa/connect-ui/primitives';

  import { addJob, dismissInboxItem, getInbox, setInboxAuto, type Inbox, type InboxItem } from './api';
  import { heldHost } from './host';
  import { formatClock, formatNumber, queueErrorText } from './i18n';

  interface Props {
    deviceId: string;
    /** Called after the queue changed, so that it is read again. */
    onchange: () => void;
  }

  let { deviceId, onchange }: Props = $props();

  const host = heldHost();
  const t = host.t;

  let inbox = $state<Inbox | undefined>(undefined);
  let busy = $state(false);

  const load = async (): Promise<void> => {
    try {
      inbox = await getInbox(deviceId, host.signal);
    } catch {
      // Not allowed, or the module is starting: the next turn tries again.
      if (host.signal.aborted) return;
    }
  };

  void load();
  const timer = setInterval(() => void load(), 10_000);
  host.signal.addEventListener('abort', () => clearInterval(timer), { once: true });
  onDestroy(() => clearInterval(timer));

  const size = (bytes: number): string =>
    bytes >= 1 << 20
      ? `${formatNumber(bytes / (1 << 20), { maximumFractionDigits: 1 })} MiB`
      : `${formatNumber(Math.max(1, Math.round(bytes / 1024)))} KiB`;

  const add = async (item: InboxItem): Promise<void> => {
    if (inbox === undefined) return;
    busy = true;
    try {
      const job = await addJob(deviceId, inbox.storage, item.path, item.name);
      toast.success(t('print-queue.fileAction.added', { name: job.name }));
      await dismissInboxItem(deviceId, item.path);
      onchange();
    } catch (error) {
      toast.error(queueErrorText(error, t));
    } finally {
      busy = false;
      await load();
    }
  };

  const ignore = async (item: InboxItem): Promise<void> => {
    busy = true;
    try {
      await dismissInboxItem(deviceId, item.path);
      toast.success(t('print-queue.inbox.ignored', { name: item.name }));
    } catch (error) {
      toast.error(queueErrorText(error, t));
    } finally {
      busy = false;
      await load();
    }
  };

  const toggleAuto = async (enabled: boolean): Promise<void> => {
    busy = true;
    try {
      await setInboxAuto(deviceId, enabled);
    } catch (error) {
      toast.error(queueErrorText(error, t));
    } finally {
      busy = false;
      await load();
    }
  };
</script>

{#if inbox !== undefined}
  <div class="mb-3 flex flex-col gap-2 rounded-md border border-border p-3" data-part="inbox">
    <div class="flex flex-wrap items-center justify-between gap-2">
      <div class="flex items-center gap-2">
        <span class="text-sm font-medium">{t('print-queue.inbox.title')}</span>
        {#if inbox.items.length > 0}
          <Badge variant="outline">{t('print-queue.inbox.count', { count: formatNumber(inbox.items.length) })}</Badge>
        {/if}
      </div>
      <label class="flex items-center gap-2 text-xs text-muted-foreground">
        <input
          type="checkbox"
          checked={inbox.auto}
          disabled={busy}
          onchange={(event: Event) => void toggleAuto((event.currentTarget as HTMLInputElement).checked)}
        />
        {t('print-queue.inbox.auto')}
      </label>
    </div>

    {#if inbox.items.length > 0}
      <ul class="flex flex-col divide-y divide-border">
        {#each inbox.items as item (item.path)}
          <li class="flex flex-wrap items-center gap-3 py-2">
            <span class="min-w-0 flex-1">
              <span class="block truncate text-sm" title={item.name}>{item.name}</span>
              <span class="block text-xs text-muted-foreground">
                {formatClock(new Date(item.modified).getTime(), Date.now())} · {size(item.size)}
              </span>
            </span>
            <Button size="sm" disabled={busy} onclick={() => void add(item)}>+ {t('print-queue.inbox.add')}</Button>
            <Button size="sm" variant="ghost" disabled={busy} onclick={() => void ignore(item)}>
              {t('print-queue.inbox.ignore')}
            </Button>
          </li>
        {/each}
      </ul>
    {:else}
      <p class="text-xs text-muted-foreground">
        {t('print-queue.inbox.folder', { folder: inbox.folder, storage: inbox.storage })}
        {inbox.auto ? '' : t('print-queue.inbox.autoHint')}
      </p>
    {/if}
  </div>
{/if}
