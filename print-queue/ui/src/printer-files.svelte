<!--
  Side panel to add files to the queue, from the printer's drive or from a
  storage of the server. A file PrusaSlicer sends through the PrusaSlicer 2
  Connector lands on the printer's drive, and in a storage when the printer's
  Save to storage switch is on.

  It opens over the right of the page, so the queue stays in sight and shows
  each added job at once. It closes with its cross, Escape, a click beside it
  or the “Done” button. A click on a file adds it, and the panel stays
  open to add more.
-->
<script lang="ts">
  import { SOURCE_LOCALE } from '@prusa/connect-ui/i18n';
  import {
    Button,
    Input,
    Sheet,
    SheetClose,
    SheetContent,
    SheetDescription,
    SheetFooter,
    SheetHeader,
    SheetTitle,
    toast,
  } from '@prusa/connect-ui/primitives';

  import {
    addJob,
    listPrinterFiles,
    listStorageFolder,
    listStorages,
    thumbnailUrl,
  } from './api';
  import { heldHost } from './host';
  import { queueErrorText } from './i18n';

  interface Props {
    deviceId: string;
    printerName: string;
    /** How many times each file is already waiting or printing, by `storage:path`. */
    queued: Record<string, number>;
    onadded: () => void;
    onclose: () => void;
  }

  let { deviceId, printerName, queued, onadded, onclose }: Props = $props();

  const t = heldHost().t;

  /** One row of the list, whether it comes from the printer or from a storage. */
  interface Entry {
    name: string;
    label: string;
    path: string;
    folder: boolean;
    modified?: string | undefined;
  }

  type Source = 'printer' | 'storage';

  let open = $state(true);
  let source = $state<Source>('printer');
  // The storage being read; empty while the list of storages is shown.
  let storage = $state('');
  let folder = $state('');
  let items = $state<Entry[]>([]);
  let loading = $state(true);
  let failure = $state<{ error: unknown } | undefined>(undefined);
  let search = $state('');
  let adding = $state<string | undefined>(undefined);
  let justAdded = $state<Record<string, boolean>>({});

  const printableName = (name: string): boolean => /\.(bgcode|gcode|bbf|gco|g)$/iu.test(name);
  // The printer names a long file twice: a short 8.3 name and the long one.
  const printable = (entry: Entry): boolean =>
    printableName(entry.label) || /\.(BGC|GCO|BBF)$/u.test(entry.name);

  const clean = (raw: string): string => raw.replace(/^\/+/u, '');

  const newestFirst = (a: Entry, b: Entry): number =>
    a.folder === b.folder ? (b.modified ?? '').localeCompare(a.modified ?? '') : a.folder ? -1 : 1;

  const loadPrinter = async (path: string, refresh: boolean): Promise<Entry[]> => {
    let answer = await listPrinterFiles(deviceId, path, refresh);
    // The core reads a folder in the background: ask again while it reads.
    for (let i = 0; i < 10 && answer.reading && answer.items.length === 0; i++) {
      await new Promise((resolve) => setTimeout(resolve, 700));
      answer = await listPrinterFiles(deviceId, path);
    }
    return answer.items.map((file) => ({
      name: file.name,
      label: file.display_name || file.name,
      path: clean(file.path || (path ? `${path}/${file.name}` : file.name)),
      folder: file.folder === true,
      modified: file.modified,
    }));
  };

  const loadStorage = async (name: string, path: string): Promise<Entry[]> => {
    if (name === '') {
      // The storages play the part of the drives of a printer.
      const storages = await listStorages();
      return storages
        .filter((entry) => entry.browse !== false)
        .map((entry) => ({
          name: entry.name,
          label: entry.display_name || entry.name,
          path: entry.name,
          folder: true,
        }));
    }
    const files = await listStorageFolder(name, path);
    return files.map((file) => ({
      name: file.name,
      label: file.name,
      path: clean(file.path || (path ? `${path}/${file.name}` : file.name)),
      folder: file.folder === true,
      modified: file.modified,
    }));
  };

  const load = async (path: string, refresh = false, inStorage = storage): Promise<void> => {
    loading = true;
    failure = undefined;
    search = '';
    try {
      const entries = source === 'printer' ? await loadPrinter(path, refresh) : await loadStorage(inStorage, path);
      folder = path;
      storage = inStorage;
      items = entries
        .filter((entry) => entry.folder || printable(entry) || (source === 'printer' && path === ''))
        .sort(newestFirst);
    } catch (error) {
      failure = { error };
    } finally {
      loading = false;
    }
  };

  // A row of a folder opens it. In a storage, a row of the list of storages
  // opens that storage at its root.
  const openFolder = (entry: Entry): Promise<void> =>
    source === 'storage' && storage === '' ? load('', false, entry.name) : load(entry.path);

  // Start at the drive list, and open the drive when there is only one.
  const start = async (): Promise<void> => {
    await load('', true);
    const drives = items.filter((entry) => entry.folder);
    if (drives.length === 1 && drives[0] !== undefined) await openFolder(drives[0]);
  };
  void start();

  const choose = async (next: Source): Promise<void> => {
    if (next === source) return;
    source = next;
    storage = '';
    folder = '';
    items = [];
    await start();
  };

  const goUp = (): Promise<void> => {
    if (folder === '' && source === 'storage') return load('', false, '');
    return load(folder.includes('/') ? folder.slice(0, folder.lastIndexOf('/')) : '');
  };

  // A printer starts at its drive list, which has no parent; a storage has its
  // list of storages above its root.
  const canGoUp = $derived(folder !== '' || (source === 'storage' && storage !== ''));

  const place = $derived(
    source === 'storage' && storage !== '' ? `${storage}:/${folder}` : folder || '/',
  );

  const shown = $derived.by(() => {
    const needle = search.trim().toLowerCase();
    return needle === '' ? items : items.filter((entry) => entry.label.toLowerCase().includes(needle));
  });

  const add = async (entry: Entry): Promise<void> => {
    const key = `${source === 'storage' ? storage : ''}:${entry.path}`;
    adding = key;
    try {
      const job = await addJob(deviceId, source === 'storage' ? storage : '', entry.path, entry.label);
      toast.success(t('print-queue.fileAction.added', { name: job.name }));
      justAdded = { ...justAdded, [key]: true };
      setTimeout(() => {
        justAdded = { ...justAdded, [key]: false };
      }, 2000);
      onadded();
    } catch (error) {
      toast.error(queueErrorText(error, t));
    } finally {
      adding = undefined;
    }
  };
</script>

<Sheet
  bind:open
  onOpenChange={(next: boolean) => {
    if (!next) onclose();
  }}
>
  <SheetContent lang={SOURCE_LOCALE} showCloseButton={false} side="right" class="flex w-full flex-col gap-3 sm:max-w-lg!">
    <SheetClose aria-label={t('print-queue.pick.close')} class="absolute top-4 right-4 rounded-sm px-1 text-muted-foreground hover:text-foreground focus-visible:ring-2 focus-visible:ring-ring">
      <span aria-hidden="true">×</span>
    </SheetClose>
    <SheetHeader>
      <SheetTitle>{t('print-queue.pick.title')} · {printerName}</SheetTitle>
      <SheetDescription>
        {source === 'storage' ? t('print-queue.pick.hintStorage') : t('print-queue.pick.hint')}
      </SheetDescription>
    </SheetHeader>

    <div class="flex items-center gap-2 px-4" role="group" aria-label={t('print-queue.pick.source')}>
      <Button
        size="sm"
        variant="ghost"
        class={source === 'printer' ? 'bg-muted' : ''}
        aria-pressed={source === 'printer'}
        onclick={() => choose('printer')}
      >
        {t('print-queue.pick.sourcePrinter')}
      </Button>
      <Button
        size="sm"
        variant="ghost"
        class={source === 'storage' ? 'bg-muted' : ''}
        aria-pressed={source === 'storage'}
        onclick={() => choose('storage')}
      >
        {t('print-queue.pick.sourceStorage')}
      </Button>
    </div>

    <div class="flex items-center gap-2 px-4">
      {#if canGoUp}
        <Button size="sm" variant="ghost" onclick={goUp} aria-label={t('print-queue.pick.up')}>↩</Button>
      {/if}
      <span class="min-w-0 flex-1 truncate text-xs text-muted-foreground" title={place}>
        📁 {place}
      </span>
      <Button size="sm" variant="ghost" onclick={() => load(folder, true)} aria-label={t('print-queue.pick.refresh')}>⟳</Button>
    </div>
    <div class="px-4">
      <Input
        type="search"
        placeholder={t('print-queue.pick.search')}
        value={search}
        oninput={(event: Event) => (search = (event.currentTarget as HTMLInputElement).value)}
      />
    </div>

    <div class="min-h-0 flex-1 overflow-y-auto px-4">
      {#if failure}
        <p class="text-sm text-destructive">{queueErrorText(failure.error, t)}</p>
      {:else if loading}
        <p class="text-sm text-muted-foreground">{source === 'storage' ? t('print-queue.pick.loadingStorage') : t('print-queue.pick.loading')}</p>
      {:else if shown.length === 0}
        <p class="text-sm text-muted-foreground">{t('print-queue.pick.empty')}</p>
      {:else}
        <ul class="flex flex-col gap-1">
          {#each shown as entry (entry.path || entry.name)}
            {@const key = `${source === 'storage' ? storage : ''}:${entry.path}`}
            {@const count = queued[key] ?? 0}
            <li>
              {#if entry.folder}
                <button
                  class="flex w-full items-center gap-3 rounded-md p-2 text-start text-sm hover:bg-muted"
                  onclick={() => openFolder(entry)}
                >
                  <span class="text-lg">📁</span>
                  <span class="min-w-0 flex-1 truncate">{entry.label}</span>
                  <span class="text-muted-foreground">›</span>
                </button>
              {:else}
                <button
                  class="flex w-full items-center gap-3 rounded-md p-2 text-start hover:bg-muted disabled:opacity-60"
                  disabled={adding !== undefined}
                  onclick={() => add(entry)}
                  title={entry.label}
                >
                  <img
                    src={thumbnailUrl(deviceId, source === 'storage' ? storage : '', entry.path)}
                    alt=""
                    loading="lazy"
                    class="size-14 shrink-0 rounded bg-muted object-contain"
                    onerror={(event) => ((event.currentTarget as HTMLImageElement).style.visibility = 'hidden')}
                  />
                  <span class="flex min-w-0 flex-1 flex-col">
                    <span class="truncate text-sm">{entry.label}</span>
                    {#if count > 0}
                      <span class="text-xs text-muted-foreground">
                        {t('print-queue.pick.inQueue')}{count > 1 ? ` ×${count}` : ''}
                      </span>
                    {/if}
                  </span>
                  <span
                    class={[
                      'shrink-0 rounded-md px-2 py-1 text-xs font-medium',
                      justAdded[key] ? 'bg-success text-success-foreground' : 'bg-primary text-primary-foreground',
                    ]}
                  >
                    {justAdded[key] ? `✓ ${t('print-queue.pick.added')}` : `+ ${t('print-queue.pick.add')}`}
                  </span>
                </button>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    </div>

    <SheetFooter class="px-4 pb-4">
      <Button onclick={onclose}>{t('print-queue.pick.done')}</Button>
    </SheetFooter>
  </SheetContent>
</Sheet>
