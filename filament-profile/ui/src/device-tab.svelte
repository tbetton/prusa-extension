<script lang="ts">
  import { onMount } from 'svelte';
  import {
    Badge,
    Button,
    Card,
    CardContent,
    CardHeader,
    CardTitle,
    EmptyState,
    toast,
  } from '@prusa/connect-ui/primitives';

  import {
    EDITABLE,
    getCatalogue,
    getMaterial,
    getSettings,
    getState,
    listAllPresets,
    listPrinters,
    listTools,
    matchFilament,
    ProfileError,
    putState,
    refreshCatalogue,
    writeBundle,
    type Candidate,
    type Printer,
    type Properties,
    type Request,
    type SavedState,
    type Status,
    type ToolSource,
    type Tool,
  } from './api';
  import { heldHost } from './host';
  import { compareToolIds, toolName } from './labels';
  import { nozzleToken } from './variants';

  interface Props {
    /** The device this tab is about. It is the only prop a tab receives. */
    deviceId: string;
  }

  let { deviceId }: Props = $props();

  const host = heldHost();
  const t = host.t;

  interface Row {
    tool: Tool;
    brand: string;
    material: string;
    type: string;
    color: string;
    include: boolean;
    profileName: string;
    /** The profiles that look like the spool, offered first. */
    best: string[];
    preset: string;
    query: string;
    /** What the profile would be written with, and what the person changed. */
    settings: Record<string, string>;
    edits: Record<string, string>;
    section: string;
    adaptedTo: string;
    reading: boolean;
    /** What the catalogue says of the material, when it says anything useful. */
    properties: Properties | undefined;
    /** Undefined follows the preset: the brand's temperatures replace a generic preset's. */
    useProperties: boolean | undefined;
  }

  let rows = $state<Row[]>([]);
  /** Every profile on offer, whatever the printer. */
  let allPresets = $state<Candidate[]>([]);
  /** What was chosen last time on this printer's tab, kept by the module. */
  let saved: SavedState = {};
  /** Set once the saved choices are back: only then does the tab keep its own. */
  let loaded = $state(false);
  /** The tool whose settings are open. */
  let openId = $state('');
  /** Every tool of the printer, loaded or not: one alone is the extruder. */
  let toolCount = $state(0);
  /** Where the tools were read: Materials knows the brand and the product, the printer alone does not. */
  let source = $state<ToolSource>('materials');
  /** The ids of every tool in the printer's order: a tool's place is its slot in PrusaSlicer. */
  let order = $state<string[]>([]);
  let status = $state<Status | undefined>(undefined);
  /** The printer preset of PrusaSlicer the bundle forces, and what is known of it. */
  let printer = $state('');
  let printerInfo = $state<Printer | undefined>(undefined);
  let printers = $state<Printer[]>([]);
  let printerQuery = $state('');
  let loading = $state(true);
  let matching = $state(false);
  let busy = $state(false);
  let problem = $state<string | undefined>(undefined);

  const printerKey = `filament-profile.printer.${deviceId}`;

  const message = (error: unknown): string => {
    if (error instanceof ProfileError) {
      if (error.status === 404 && error.code === undefined) return t('filament-profile.fault.noMaterials');
      if (error.code) {
        const code = error.code.slice(error.code.lastIndexOf('/') + 1);
        const key = `filament-profile.fault.${code}`;
        const text = t(key);
        if (text !== key) return error.detail ? `${text} ${error.detail}` : text;
      }
      return t('filament-profile.fault.http', { status: String(error.status) });
    }
    return t('filament-profile.fault.unknown');
  };

  const profileNameOf = (brand: string, material: string): string =>
    brand !== '' && !material.toLowerCase().startsWith(brand.toLowerCase())
      ? `${brand} ${material}`
      : material;

  const rowOf = (tool: Tool): Row | undefined => {
    const brand = tool.assignment?.brand_name ?? '';
    const material = tool.assignment?.material_name ?? tool.reported?.abbreviation ?? '';
    const type = tool.assignment?.type || tool.reported?.type || '';
    if (material === '' && type === '') return undefined;

    return {
      tool,
      brand,
      material,
      type,
      color: tool.assignment?.color_rgba || tool.reported?.color_rgba || '',
      include: true,
      profileName: profileNameOf(brand, material || type),
      best: [],
      preset: '',
      query: '',
      settings: {},
      edits: {},
      section: '',
      adaptedTo: '',
      reading: false,
      properties: undefined,
      useProperties: undefined,
    };
  };

  const labelOf = (row: Row): string => {
    const name = toolName(row.tool.id, toolCount === 1);
    if (name.tool === undefined) return t('filament-profile.extruder');
    if (name.slot === undefined) return t('filament-profile.tool', { n: String(name.tool) });

    return t('filament-profile.toolSlot', { n: String(name.tool), slot: String(name.slot) });
  };

  const swatch = (color: string): string | undefined =>
    /^#[0-9a-f]{6}(?:[0-9a-f]{2})?$/iu.test(color) ? color : undefined;

  /** Names the spool a choice was made for: another spool drops the choice. */
  const spoolKey = (row: Row): string => `${row.brand}|${row.material}|${row.type}`;

  const slotOf = (row: Row): number => order.indexOf(row.tool.id) + 1;

  const hasTemperatures = (p: Properties | undefined): boolean =>
    p !== undefined &&
    ((p.min_print_temp ?? 0) > 0 || (p.min_bed_temp ?? 0) > 0 || (p.density ?? 0) > 0);

  /** Whether the profile takes the catalogue's temperatures: a generic preset does, a brand's own does not. */
  const useOf = (row: Row): boolean =>
    hasTemperatures(row.properties) && (row.useProperties ?? row.preset.startsWith('Generic '));

  const summaryOf = (p: Properties): string =>
    [
      (p.min_print_temp ?? 0) > 0 ? `${String(p.min_print_temp)}–${String(p.max_print_temp)} °C` : '',
      (p.min_bed_temp ?? 0) > 0 ? `${t('filament-profile.bed')} ${String(p.min_bed_temp)}–${String(p.max_bed_temp)} °C` : '',
      (p.density ?? 0) > 0 ? `${String(p.density)} g/cm³` : '',
    ]
      .filter((part) => part !== '')
      .join(' · ');

  const printerLabel = (p: Printer): string =>
    `${String(p.nozzle_diameter ?? '')}${p.high_flow ? ' HF' : ''}`.trim();

  /** A profile in the list: its name, and its source when it is not Prusa's. */
  const presetLabel = (candidate: Candidate): string =>
    candidate.source !== undefined && candidate.source !== 'prusa'
      ? `${candidate.preset} · ${candidate.source}`
      : candidate.preset;

  const shown = (row: Row, key: string): string => row.edits[key] ?? row.settings[key] ?? '';

  const edit = (row: Row, key: string, value: string): void => {
    if (value === (row.settings[key] ?? '')) delete row.edits[key];
    else row.edits[key] = value;
  };

  // A newer reading of a tool's settings replaces an older one still on its way.
  const readings = new Map<string, number>();

  const readSettings = async (row: Row): Promise<void> => {
    if (row.preset === '') return;
    const turn = (readings.get(row.tool.id) ?? 0) + 1;
    readings.set(row.tool.id, turn);
    row.reading = true;
    try {
      const found = await getSettings(
        row.preset,
        printer,
        useOf(row) ? row.properties : undefined,
        host.signal,
      );
      if (readings.get(row.tool.id) !== turn) return;
      row.settings = found.settings;
      row.section = found.section;
      row.adaptedTo = found.adapted ?? '';
    } catch (error) {
      if (readings.get(row.tool.id) === turn) problem = message(error);
    } finally {
      if (readings.get(row.tool.id) === turn) row.reading = false;
    }
  };

  const pick = (row: Row, preset: string): void => {
    row.preset = preset;
    row.edits = {};
    row.useProperties = undefined;
    void readSettings(row);
  };

  const useMaker = (row: Row, on: boolean): void => {
    row.useProperties = on;
    row.edits = {};
    void readSettings(row);
  };

  const listed = (row: Row): Candidate[] => {
    const words = row.query.toLowerCase().split(/\s+/u).filter((word) => word !== '');
    const found = allPresets.filter((candidate) => {
      const name = candidate.preset.toLowerCase();

      return words.every((word) => name.includes(word));
    });
    if (words.length > 0) return found.slice(0, 150);

    // Without a search, only what may suit the spool: its nearest profiles and the generic one of its type.
    const generic = `generic ${row.type.toLowerCase()}`;
    const names = [...row.best, ...allPresets.map((c) => c.preset).filter((n) => n.toLowerCase() === generic)];

    return [...new Set(names)].flatMap((name) => allPresets.filter((candidate) => candidate.preset === name));
  };

  const matchAll = async (): Promise<void> => {
    matching = true;
    try {
      await Promise.all(
        rows.map(async (row) => {
          try {
            const found = await matchFilament(row.brand, row.material, row.type, '', host.signal);
            row.best = found.map((candidate) => candidate.preset);
            if (row.preset === '') row.preset = row.best[0] ?? '';
            await readSettings(row);
          } catch (error) {
            problem = message(error);
          }
        }),
      );
    } finally {
      matching = false;
    }
  };

  const loadProperties = async (): Promise<void> => {
    await Promise.all(
      rows.map(async (row) => {
        const uuid = row.tool.assignment?.material_uuid ?? '';
        if (uuid === '') return;
        try {
          const found = await getMaterial(uuid, host.signal);
          if (hasTemperatures(found)) row.properties = found;
        } catch {
          // A material the catalogue does not know keeps the preset's values.
        }
      }),
    );
  };

  // Every choice is kept as it is made, to come back after a change of page or a restart.
  $effect(() => {
    if (!loaded) return;
    const state: SavedState = {
      printer,
      tools: Object.fromEntries(
        rows.map((row) => [
          row.tool.id,
          {
            spool: spoolKey(row),
            include: row.include,
            name: row.profileName,
            preset: row.preset,
            edits: { ...row.edits },
            ...(row.useProperties === undefined ? {} : { useProperties: row.useProperties }),
          },
        ]),
      ),
    };
    const body = JSON.stringify(state);
    const timer = setTimeout(() => void putState(deviceId, JSON.parse(body) as SavedState).catch((error: unknown) => (problem = message(error))), 300);

    return () => clearTimeout(timer);
  });

  // The list of printers follows what is typed.
  $effect(() => {
    const query = printerQuery.trim();
    if (!status?.ready || query === '') return;
    const timer = setTimeout(() => void searchPrinters(), 200);

    return () => clearTimeout(timer);
  });

  const searchPrinters = async (): Promise<void> => {
    try {
      printers = await listPrinters(printerQuery.trim(), host.signal);
    } catch (error) {
      problem = message(error);
    }
  };

  const remember = (value: string): void => {
    try {
      localStorage.setItem(printerKey, value);
    } catch {
      // Storage may be blocked; the choice then lasts for this visit only.
    }
  };

  const choosePrinter = async (name: string): Promise<void> => {
    printer = name;
    printerInfo = name === '' ? undefined : (printers.find((p) => p.name === name) ?? printerInfo);
    remember(name);
    await Promise.all(rows.map((row) => readSettings(row)));
  };

  // The printer chosen last time, with what is known of it.
  const restorePrinter = async (): Promise<void> => {
    let stored = saved.printer ?? '';
    try {
      if (stored === '') stored = localStorage.getItem(printerKey) ?? '';
    } catch {
      // See above.
    }

    if (stored === '') return;

    try {
      const found = await listPrinters(stored, host.signal);
      const exact = found.find((p) => p.name === stored);
      if (exact) {
        printer = stored;
        printerInfo = exact;
      }
    } catch {
      // The printer list is not there yet: the choice is made again.
    }
  };

  const load = async (): Promise<void> => {
    loading = true;
    problem = undefined;
    try {
      const [found, state] = await Promise.all([
        listTools(deviceId, host.signal),
        getCatalogue(host.signal),
      ]);
      const tools = found.tools;
      source = found.source;
      status = state;
      toolCount = tools.length;
      order = tools.map((tool) => tool.id).sort(compareToolIds);
      rows = tools.flatMap((tool) => rowOf(tool) ?? []);
      saved = await getState(deviceId, host.signal).catch((error: unknown): SavedState => {
        problem = message(error);

        return {};
      });
      for (const row of rows) {
        const kept = saved.tools?.[row.tool.id];
        if (kept?.spool !== spoolKey(row)) continue;
        row.include = kept.include;
        row.profileName = kept.name;
        row.preset = kept.preset;
        row.edits = { ...kept.edits };
        row.useProperties = kept.useProperties;
      }

      await loadProperties();
      if (state.ready) {
        await restorePrinter();
        await searchPrinters();
        allPresets = await listAllPresets(host.signal);
        await matchAll();
      }
      openId = rows[0]?.tool.id ?? '';
      loaded = true;
    } catch (error) {
      problem = message(error);
    } finally {
      loading = false;
    }
  };

  // While the first download runs, ask again until the profiles are there.
  const wait = async (): Promise<void> => {
    while (!host.signal.aborted && status && !status.ready) {
      await new Promise((resolve) => setTimeout(resolve, 3000));
      try {
        status = await getCatalogue(host.signal);
        if (status.ready) {
          await restorePrinter();
          await searchPrinters();
          allPresets = await listAllPresets(host.signal);
          await matchAll();
        }
      } catch {
        return;
      }
    }
  };

  onMount(() => {
    void load().then(wait);
  });

  const refresh = async (): Promise<void> => {
    try {
      status = await refreshCatalogue();
      await wait();
    } catch (error) {
      problem = message(error);
    }
  };

  const download = async (): Promise<void> => {
    busy = true;
    problem = undefined;
    try {
      const filaments: Request[] = rows
        .filter((row) => row.include && row.preset !== '')
        .map((row) => {
          const request: Request = {
            name: row.profileName,
            slot: slotOf(row),
            base: row.preset,
          };
          if (row.brand !== '') request.vendor = row.brand;
          if (row.color !== '') request.color_rgba = row.color;
          if (useOf(row) && row.properties) request.properties = row.properties;
          if (Object.keys(row.edits).length > 0) request.overrides = { ...row.edits };
          return request;
        });
      const text = await writeBundle(printer, filaments);
      const url = URL.createObjectURL(new Blob([text], { type: 'text/plain;charset=utf-8' }));
      const link = document.createElement('a');
      link.href = url;
      link.download = 'filament-profile.ini';
      link.click();
      URL.revokeObjectURL(url);
      toast.success(t('filament-profile.downloaded', { count: String(filaments.length) }));
    } catch (error) {
      problem = message(error);
    } finally {
      busy = false;
    }
  };

  const sourcesLine = $derived(
    Object.entries(status?.sources ?? {})
      .filter(([id]) => status?.enabled?.includes(id) === true)
      .map(([id, count]) => `${id} ${String(count)}`)
      .join(' · '),
  );

  // PrusaSlicer reads the selection only when filament 1 is among the profiles.
  const slotGap = $derived(
    rows.some((row) => row.include && row.preset !== '') &&
      !rows.some((row) => row.include && row.preset !== '' && slotOf(row) === 1),
  );

  const tooManyTools = $derived(
    printerInfo !== undefined &&
      rows.some((row) => row.include && row.preset !== '' && slotOf(row) > (printerInfo?.extruders ?? 0)),
  );

  const chosen = $derived(rows.filter((row) => row.include && row.preset !== '').length);
</script>

<div class="flex flex-col gap-4">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <p class="text-sm text-muted-foreground">
      {#if status?.ready}
        {t('filament-profile.catalogue', { version: status.version ?? '', count: String(status.presets) })}
        {#if sourcesLine !== ''}
          {t('filament-profile.sources', { list: sourcesLine })}
        {/if}
      {:else if status}
        {t('filament-profile.downloading')}
      {/if}
    </p>
    <Button size="sm" variant="ghost" disabled={status?.refreshing === true} onclick={refresh}>
      {t('filament-profile.refresh')}
    </Button>
  </div>

  {#if source === 'printer' && !loading}
    <p class="text-sm text-muted-foreground">{t('filament-profile.printerOnly')}</p>
  {/if}

  {#if status?.error}
    <p class="text-sm text-destructive">{status.error}</p>
  {/if}
  {#if problem}
    <p class="text-sm text-destructive" role="alert">{problem}</p>
  {/if}

  {#if loading}
    <p class="text-sm text-muted-foreground">{t('filament-profile.loading')}</p>
  {:else if rows.length === 0 && !problem}
    <EmptyState size="sm" heading={t('filament-profile.empty.title')} description={t('filament-profile.empty.body')} />
  {:else if rows.length > 0}
    <div class="flex flex-col gap-2">
      <label class="text-sm font-medium" for="fp-printer">{t('filament-profile.printer')}</label>
      <input
        id="fp-printer"
        class="h-8 min-w-0 rounded-md border border-input bg-background px-2 text-sm"
        placeholder={t('filament-profile.printerSearch')}
        autocomplete="off"
        bind:value={printerQuery}
      />
      {#if printerQuery.trim() !== ''}
        <ul class="max-h-60 overflow-y-auto rounded-md border border-input" role="listbox">
          {#each printers as option (option.name)}
            <li role="option" aria-selected={option.name === printer}>
              <button
                type="button"
                class="w-full px-2 py-1 text-left text-sm hover:bg-accent {option.name === printer
                  ? 'bg-accent font-medium'
                  : ''}"
                onclick={() => {
                  void choosePrinter(option.name);
                  printerQuery = '';
                }}
              >
                {option.name} ({printerLabel(option)})
              </button>
            </li>
          {:else}
            <li class="px-2 py-1 text-sm text-muted-foreground">
              {t('filament-profile.noPrinter', { query: printerQuery.trim() })}
            </li>
          {/each}
        </ul>
      {:else if printer !== ''}
        <p class="text-sm">
          {t('filament-profile.printerChosen')} <span class="font-medium">{printer}</span>
        </p>
      {/if}
      <p class="text-xs text-muted-foreground">
        {printer === '' ? t('filament-profile.needPrinter') : t('filament-profile.printerHint')}
      </p>
      {#if tooManyTools}
        <p class="text-xs text-destructive">
          {t('filament-profile.fewExtruders', { count: String(printerInfo?.extruders ?? 0) })}
        </p>
      {/if}
    </div>

    <div class="flex flex-col gap-2">
      {#each rows as row (row.tool.id)}
        {@const color = swatch(row.color)}
        {@const open = openId === row.tool.id}
        <Card class="min-w-0">
          <div class="flex items-center gap-3 px-4 py-3">
            <input type="checkbox" bind:checked={row.include} aria-label={labelOf(row)} />
            <button
              type="button"
              class="flex min-w-0 flex-1 items-center gap-3 text-left"
              aria-expanded={open}
              onclick={() => (openId = open ? '' : row.tool.id)}
            >
              <span class="shrink-0 font-semibold">{labelOf(row)}</span>
              {#if color}
                <span class="size-4 shrink-0 rounded-full border border-border" style:background-color={color}></span>
              {/if}
              <Badge variant="outline">{row.type}</Badge>
              <Badge variant="outline">{nozzleToken(row.tool.nozzle)}</Badge>
              <span class="min-w-0 flex-1 truncate text-sm text-muted-foreground">
                {[row.brand, row.material].filter(Boolean).join(' · ')}
              </span>
              <span class="shrink-0 text-muted-foreground" aria-hidden="true">{open ? '▾' : '▸'}</span>
            </button>
          </div>
          {#if open}
            <CardContent class="grid gap-4 border-t border-border pt-4 md:grid-cols-2">
              <div class="flex min-w-0 flex-col gap-2">
                <label class="flex flex-col gap-1 text-xs text-muted-foreground">
                  {t('filament-profile.name')}
                  <input
                    class="h-8 rounded-md border border-input bg-background px-2 text-sm text-foreground"
                    bind:value={row.profileName}
                  />
                </label>
                <input
                  class="h-8 rounded-md border border-input bg-background px-2 text-sm"
                  aria-label={t('filament-profile.searchPresets')}
                  placeholder={t('filament-profile.searchPresets')}
                  bind:value={row.query}
                />
                {#if true}
                  {@const shownList = listed(row)}
                  <p class="text-xs text-muted-foreground">
                    {t('filament-profile.profiles', { count: String(shownList.length) })}
                  </p>
                  <ul class="max-h-72 overflow-y-auto rounded-md border border-input" role="listbox">
                    {#each shownList as candidate (candidate.preset)}
                      <li role="option" aria-selected={candidate.preset === row.preset}>
                        <button
                          type="button"
                          class="w-full px-2 py-1 text-left text-sm hover:bg-accent {candidate.preset === row.preset
                            ? 'bg-accent font-medium'
                            : ''}"
                          onclick={() => pick(row, candidate.preset)}
                        >
                          {presetLabel(candidate)}
                        </button>
                      </li>
                    {:else}
                      <li class="px-2 py-1 text-sm text-muted-foreground">
                        {t('filament-profile.noMatch', { query: row.query.trim() })}
                      </li>
                    {/each}
                  </ul>
                {/if}
              </div>

              <div class="flex min-w-0 flex-col gap-2">
                <p class="text-sm font-medium">{t('filament-profile.settings')}</p>
                <p class="text-xs text-muted-foreground">{t('filament-profile.settingsHint')}</p>
                {#if row.preset === ''}
                  <p class="text-xs text-muted-foreground">{t('filament-profile.settingsLoading')}</p>
                {:else}
                  <p class="text-xs text-muted-foreground">
                    {t('filament-profile.settingsFrom', { section: row.section || row.preset })}
                  </p>
                  {#if row.properties}
                    <label class="flex items-center gap-2 text-xs text-muted-foreground">
                      <input
                        type="checkbox"
                        checked={useOf(row)}
                        onchange={(event) => useMaker(row, event.currentTarget.checked)}
                      />
                      {t('filament-profile.useCatalogue')} ({summaryOf(row.properties)})
                    </label>
                  {/if}
                  <div class="grid grid-cols-2 gap-2" aria-busy={row.reading}>
                    {#each EDITABLE as key (key)}
                      <label class="flex flex-col gap-1 text-xs text-muted-foreground">
                        {t(`filament-profile.set.${key}`)}
                        <input
                          class="h-8 rounded-md border bg-background px-2 text-sm text-foreground {key in row.edits
                            ? 'border-primary'
                            : 'border-input'}"
                          value={shown(row, key)}
                          oninput={(event) => edit(row, key, event.currentTarget.value)}
                        />
                      </label>
                    {/each}
                  </div>
                  {#if Object.keys(row.edits).length > 0}
                    <div>
                      <Button size="sm" variant="ghost" onclick={() => (row.edits = {})}>
                        {t('filament-profile.settingsReset')}
                      </Button>
                    </div>
                  {/if}
                {/if}
              </div>
            </CardContent>
          {/if}
        </Card>
      {/each}
    </div>

    <div>
      <Button disabled={busy || chosen === 0 || printer === ''} onclick={download}>
        {t('filament-profile.download', { count: String(chosen) })}
      </Button>
      <p class="mt-2 text-xs text-muted-foreground">{t('filament-profile.importHint')}</p>
      {#if slotGap}
        <p class="mt-1 text-xs text-destructive">{t('filament-profile.slotGap')}</p>
      {/if}
    </div>
  {/if}
</div>
