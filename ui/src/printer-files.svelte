<!--
  Side panel to add files from the printer's drive to the queue.

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

  import { addJob, listPrinterFiles, thumbnailUrl, type PrinterFile } from './api';
  import { heldHost } from './host';
  import { queueErrorText } from './i18n';

  interface Props {
    deviceId: string;
    printerName: string;
    /** How many times each printer path is already waiting or printing. */
    queued: Record<string, number>;
    onadded: () => void;
    onclose: () => void;
  }

  let { deviceId, printerName, queued, onadded, onclose }: Props = $props();

  const t = heldHost().t;

  let open = $state(true);
  let folder = $state('');
  let items = $state<PrinterFile[]>([]);
  let loading = $state(true);
  let failure = $state<{ error: unknown } | undefined>(undefined);
  let search = $state('');
  let adding = $state<string | undefined>(undefined);
  let justAdded = $state<Record<string, boolean>>({});

  const label = (file: PrinterFile): string => file.display_name || file.name;
  // The printer names a long file twice: a short 8.3 name and the long one.
  const printable = (file: PrinterFile): boolean =>
    /\.(bgcode|gcode|bbf|gco|g)$/iu.test(label(file)) || /\.(BGC|GCO|BBF)$/u.test(file.name);

  const pathOf = (file: PrinterFile): string => {
    const raw = file.path || (folder ? `${folder}/${file.name}` : file.name);
    return raw.replace(/^\/+/u, '');
  };

  const load = async (path: string, refresh = false): Promise<void> => {
    loading = true;
    failure = undefined;
    search = '';
    try {
      let answer = await listPrinterFiles(deviceId, path, refresh);
      // The core reads a folder in the background: ask again while it reads.
      for (let i = 0; i < 10 && answer.reading && answer.items.length === 0; i++) {
        await new Promise((resolve) => setTimeout(resolve, 700));
        answer = await listPrinterFiles(deviceId, path);
      }
      folder = path;
      items = answer.items
        .filter((file) => path === '' || file.folder || printable(file))
        .sort((a, b) =>
          a.folder === b.folder
            ? (b.modified ?? '').localeCompare(a.modified ?? '')
            : a.folder
              ? -1
              : 1,
        );
    } catch (error) {
      failure = { error };
    } finally {
      loading = false;
    }
  };

  // Start at the drive list, and open the drive when there is only one.
  const start = async (): Promise<void> => {
    await load('', true);
    const drives = items.filter((file) => file.folder);
    if (drives.length === 1 && drives[0] !== undefined) await load(pathOf(drives[0]), true);
  };
  void start();

  const parent = $derived(
    folder === '' ? undefined : folder.includes('/') ? folder.slice(0, folder.lastIndexOf('/')) : '',
  );

  const shown = $derived.by(() => {
    const needle = search.trim().toLowerCase();
    return needle === '' ? items : items.filter((file) => label(file).toLowerCase().includes(needle));
  });

  const add = async (file: PrinterFile): Promise<void> => {
    const path = pathOf(file);
    adding = path;
    try {
      const job = await addJob(deviceId, '', path, label(file));
      toast.success(t('print-queue.fileAction.added', { name: job.name }));
      justAdded = { ...justAdded, [path]: true };
      setTimeout(() => {
        justAdded = { ...justAdded, [path]: false };
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
      <SheetDescription>{t('print-queue.pick.hint')}</SheetDescription>
    </SheetHeader>

    <div class="flex items-center gap-2 px-4">
      {#if parent !== undefined}
        <Button size="sm" variant="ghost" onclick={() => load(parent)} aria-label={t('print-queue.pick.up')}>↩</Button>
      {/if}
      <span class="min-w-0 flex-1 truncate text-xs text-muted-foreground" title={folder || '/'}>
        📁 {folder || '/'}
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
        <p class="text-sm text-muted-foreground">{t('print-queue.pick.loading')}</p>
      {:else if shown.length === 0}
        <p class="text-sm text-muted-foreground">{t('print-queue.pick.empty')}</p>
      {:else}
        <ul class="flex flex-col gap-1">
          {#each shown as file (file.path || file.name)}
            {@const path = pathOf(file)}
            {@const count = queued[path] ?? 0}
            <li>
              {#if file.folder}
                <button
                  class="flex w-full items-center gap-3 rounded-md p-2 text-start text-sm hover:bg-muted"
                  onclick={() => load(path)}
                >
                  <span class="text-lg">📁</span>
                  <span class="min-w-0 flex-1 truncate">{label(file)}</span>
                  <span class="text-muted-foreground">›</span>
                </button>
              {:else}
                <button
                  class="flex w-full items-center gap-3 rounded-md p-2 text-start hover:bg-muted disabled:opacity-60"
                  disabled={adding !== undefined}
                  onclick={() => add(file)}
                  title={label(file)}
                >
                  <img
                    src={thumbnailUrl(deviceId, '', path)}
                    alt=""
                    loading="lazy"
                    class="size-14 shrink-0 rounded bg-muted object-contain"
                    onerror={(event) => ((event.currentTarget as HTMLImageElement).style.visibility = 'hidden')}
                  />
                  <span class="flex min-w-0 flex-1 flex-col">
                    <span class="truncate text-sm">{label(file)}</span>
                    {#if count > 0}
                      <span class="text-xs text-muted-foreground">
                        {t('print-queue.pick.inQueue')}{count > 1 ? ` ×${count}` : ''}
                      </span>
                    {/if}
                  </span>
                  <span
                    class={[
                      'shrink-0 rounded-md px-2 py-1 text-xs font-medium',
                      justAdded[path] ? 'bg-success text-success-foreground' : 'bg-primary text-primary-foreground',
                    ]}
                  >
                    {justAdded[path] ? `✓ ${t('print-queue.pick.added')}` : `+ ${t('print-queue.pick.add')}`}
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
