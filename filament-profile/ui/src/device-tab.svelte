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
    getCatalogue,
    listTools,
    matchFilament,
    ProfileError,
    refreshCatalogue,
    searchPresets,
    writeBundle,
    type Candidate,
    type Request,
    type Status,
    type Tool,
  } from './api';
  import { heldHost } from './host';
  import { compareToolIds, toolName } from './labels';
  import { familiesOf, nozzleToken, pickVariant } from './variants';

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
    candidates: Candidate[];
    preset: string;
    query: string;
  }

  let rows = $state<Row[]>([]);
  /** Every tool of the printer, loaded or not: one alone is the extruder. */
  let toolCount = $state(0);
  /** The ids of every tool in the printer's order: a tool's place is its slot in PrusaSlicer. */
  let order = $state<string[]>([]);
  let status = $state<Status | undefined>(undefined);
  let family = $state('');
  let loading = $state(true);
  let busy = $state(false);
  let problem = $state<string | undefined>(undefined);

  const familyKey = `filament-profile.family.${deviceId}`;

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
      candidates: [],
      preset: '',
      query: '',
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

  const slotOf = (row: Row): number => order.indexOf(row.tool.id) + 1;

  const candidateOf = (row: Row): Candidate | undefined =>
    row.candidates.find((candidate) => candidate.preset === row.preset);

  const variantOf = (row: Row): string =>
    pickVariant(candidateOf(row)?.variants ?? [], family, row.tool.nozzle);

  const families = $derived(familiesOf(rows.flatMap((row) => (row.include ? (candidateOf(row)?.variants ?? []) : []))));

  const matchAll = async (): Promise<void> => {
    await Promise.all(
      rows.map(async (row) => {
        try {
          row.candidates = await matchFilament(row.brand, row.material, row.type, host.signal);
          row.preset = row.candidates[0]?.preset ?? '';
        } catch (error) {
          problem = message(error);
        }
      }),
    );
  };

  const load = async (): Promise<void> => {
    loading = true;
    problem = undefined;
    try {
      const [tools, state] = await Promise.all([
        listTools(deviceId, host.signal),
        getCatalogue(host.signal),
      ]);
      status = state;
      toolCount = tools.length;
      order = tools.map((tool) => tool.id).sort(compareToolIds);
      rows = tools.flatMap((tool) => rowOf(tool) ?? []);
      if (state.ready) await matchAll();
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
        if (status.ready) await matchAll();
      } catch {
        return;
      }
    }
  };

  onMount(() => {
    try {
      family = localStorage.getItem(familyKey) ?? '';
    } catch {
      // Storage may be blocked; the choice then lasts for this visit only.
    }
    void load().then(wait);
  });

  const chooseFamily = (value: string): void => {
    family = value;
    try {
      localStorage.setItem(familyKey, value);
    } catch {
      // See above.
    }
  };

  const search = async (row: Row): Promise<void> => {
    try {
      const found = await searchPresets(row.query, host.signal);
      if (found.length > 0) {
        row.candidates = found;
        row.preset = found[0]?.preset ?? '';
      }
    } catch (error) {
      problem = message(error);
    }
  };

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
            variant: variantOf(row),
          };
          if (row.brand !== '') request.vendor = row.brand;
          if (row.color !== '') request.color_rgba = row.color;
          return request;
        });
      const text = await writeBundle(filaments);
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

  // PrusaSlicer reads the selection only when filament 1 is among the profiles.
  const slotGap = $derived(
    rows.some((row) => row.include && row.preset !== '') &&
      !rows.some((row) => row.include && row.preset !== '' && slotOf(row) === 1),
  );

  const chosen = $derived(rows.filter((row) => row.include && row.preset !== '').length);
</script>

<div class="flex flex-col gap-4">
  <div class="flex flex-wrap items-center justify-between gap-2">
    <p class="text-sm text-muted-foreground">
      {#if status?.ready}
        {t('filament-profile.catalogue', { version: status.version ?? '', count: String(status.presets) })}
      {:else if status}
        {t('filament-profile.downloading')}
      {/if}
    </p>
    <Button size="sm" variant="ghost" disabled={status?.refreshing === true} onclick={refresh}>
      {t('filament-profile.refresh')}
    </Button>
  </div>

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
    <div class="flex flex-wrap items-center gap-2">
      <label class="text-sm font-medium" for="fp-family">{t('filament-profile.family')}</label>
      <select
        id="fp-family"
        class="h-8 rounded-md border border-input bg-background px-2 text-sm"
        value={family}
        onchange={(event) => chooseFamily(event.currentTarget.value)}
      >
        <option value="">{t('filament-profile.familyNone')}</option>
        {#each families as option (option)}
          <option value={option}>{option}</option>
        {/each}
      </select>
    </div>

    <div class="grid gap-3 md:grid-cols-2">
      {#each rows as row (row.tool.id)}
        {@const variant = variantOf(row)}
        {@const color = swatch(row.color)}
        <Card class="min-w-0">
          <CardHeader>
            <div class="flex items-center justify-between gap-2">
              <CardTitle>
                <label class="flex items-center gap-2">
                  <input type="checkbox" bind:checked={row.include} />
                  {labelOf(row)}
                </label>
              </CardTitle>
              <div class="flex items-center gap-2">
                {#if color}
                  <span class="size-4 rounded-full border border-border" style:background-color={color}></span>
                {/if}
                <Badge variant="outline">{row.type}</Badge>
                <Badge variant="outline">{nozzleToken(row.tool.nozzle)}</Badge>
              </div>
            </div>
            <p class="text-sm text-muted-foreground">{[row.brand, row.material].filter(Boolean).join(' · ')}</p>
          </CardHeader>
          <CardContent class="flex flex-col gap-2">
            <label class="flex flex-col gap-1 text-xs text-muted-foreground">
              {t('filament-profile.name')}
              <input
                class="h-8 rounded-md border border-input bg-background px-2 text-sm text-foreground"
                bind:value={row.profileName}
              />
            </label>
            <label class="flex flex-col gap-1 text-xs text-muted-foreground">
              {t('filament-profile.preset')}
              <select
                class="h-8 rounded-md border border-input bg-background px-2 text-sm text-foreground"
                bind:value={row.preset}
                disabled={row.candidates.length === 0}
              >
                {#each row.candidates as candidate (candidate.preset)}
                  <option value={candidate.preset}>{candidate.preset}</option>
                {/each}
              </select>
            </label>
            <form
              class="flex gap-2"
              onsubmit={(event) => {
                event.preventDefault();
                void search(row);
              }}
            >
              <input
                class="h-8 min-w-0 flex-1 rounded-md border border-input bg-background px-2 text-sm"
                aria-label={t('filament-profile.search')}
                placeholder={t('filament-profile.search')}
                bind:value={row.query}
              />
              <Button size="sm" variant="secondary" type="submit" disabled={row.query.trim() === ''}>
                {t('filament-profile.searchButton')}
              </Button>
            </form>
            {#if row.preset !== ''}
              <p class="text-xs text-muted-foreground">
                {variant !== ''
                  ? t('filament-profile.variant', { variant })
                  : t('filament-profile.noVariant')}
              </p>
            {/if}
          </CardContent>
        </Card>
      {/each}
    </div>

    <div>
      <Button disabled={busy || chosen === 0} onclick={download}>
        {t('filament-profile.download', { count: String(chosen) })}
      </Button>
      <p class="mt-2 text-xs text-muted-foreground">{t('filament-profile.importHint')}</p>
      {#if slotGap}
        <p class="mt-1 text-xs text-destructive">{t('filament-profile.slotGap')}</p>
      {/if}
    </div>
  {/if}
</div>
