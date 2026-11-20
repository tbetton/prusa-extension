<script lang="ts">
  import { ApiError, type components, type PrusaConnect } from '@prusa/connect';
  import {
    Alert,
    AlertTitle,
    Button,
    Label,
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
    SettingsRow,
    Switch,
  } from '@prusa/connect-ui/primitives';

  import { moduleNamespace, platePreviewURL } from './api';
  import { heldHost } from './host';
  import PlateReference from './plate-reference.svelte';

  const host = heldHost();
  const { fleet, signal, t } = host;
  const client = host.client as PrusaConnect;
  const uid = $props.id();
  type Instance = components['schemas']['ResourceInstance'];
  type Camera = components['schemas']['Camera'];
  type Device = components['schemas']['Device'];

  let instance = $state<Instance>();
  let cameras = $state<Camera[]>([]);
  let cameraStreams = $state.raw<Record<string, string>>({});
  let printerDraft = $state<string>();
  let cameraDraft = $state<string>();
  let loading = $state(true);
  let saving = $state(false);
  let saved = $state(false);
  let error = $state('');
  let previewError = $state(false);
  let previewTick = $state(0);

  const printers = $derived(
    (fleet.devices as readonly Device[])
      .filter(
        (device) =>
          (device.kind === 'printer' || device.kind === '') && !device.retired,
      )
      .map((device) => ({
        value: device.id,
        label: device.name,
      })),
  );
  const printer = $derived(
    printers.find((option) => option.value === printerDraft)?.value ??
      (printers.length === 1 ? printers[0]?.value : undefined),
  );
  const availableCameras = $derived(
    cameras
      .filter(
        (camera) =>
          camera.device_id === printer &&
          Object.hasOwn(cameraStreams, camera.name),
      )
      .map((camera) => ({ value: camera.name, label: camera.name })),
  );
  const camera = $derived(
    availableCameras.find((option) => option.value === cameraDraft)?.value ??
      (availableCameras.length === 1 ? availableCameras[0]?.value : undefined),
  );
  const stream = $derived(camera ? cameraStreams[camera] : undefined);
  const configured = $derived(
    !!stream &&
      printer === instance?.config.plate_check_printer &&
      stream === instance?.config.plate_camera_stream,
  );

  const directAddress = (value: unknown): string | undefined => {
    if (typeof value !== 'string') return;
    const address = value.trim();
    if (address.includes('?') || address.includes('#')) return;
    const parsed = URL.parse(address);
    if (
      parsed?.protocol === 'rtsp:' &&
      parsed.hostname &&
      !parsed.username &&
      !parsed.password &&
      ['', '554', '8554'].includes(parsed.port)
    )
      return address;
  };

  const choosePrinter = (id: string): void => {
    printerDraft = id;
    cameraDraft = undefined;
    previewError = false;
    saved = false;
  };

  const writeConfig = (
    current: Instance,
    values: Record<string, unknown>,
  ): Promise<Instance> =>
    client.putResource(
      {
        path: { kind: 'sink', name: current.name },
        header: { 'If-Match': `"${current.version}"` },
        body: {
          type: current.type,
          enabled: current.enabled,
          required: current.required,
          config: { ...current.config, ...values },
          hidden_contributions: current.hidden_contributions ?? [],
          memory_mib: current.memory_mib ?? 0,
          on_over_memory: current.on_over_memory ?? 'stop',
          ...(current.switch_off === undefined
            ? {}
            : { switch_off: current.switch_off }),
        },
      },
      signal,
    );

  const writeError = (cause: unknown, fallback: string): void => {
    if (signal.aborted) return;
    error = fallback;
    if (cause instanceof ApiError && cause.status === 412) error = 'conflict';
    if (cause instanceof ApiError && cause.status === 403) error = 'forbidden';
  };

  const load = async (): Promise<void> => {
    loading = true;
    error = '';
    saved = false;
    try {
      const resources = await client.listResources(
        { query: { kind: 'sink' } },
        signal,
      );
      const instances: Instance[] = resources.items ?? [];
      const found = instances.find(
        (resource) => resource.namespace === moduleNamespace,
      );
      if (!found) {
        error = 'loadFailed';
        return;
      }
      instance = found;
      if (found.config.plate_check_enabled === false) return;
      const listed = await client.listCameras({}, signal);
      cameras = listed.items ?? [];
      const streams: [string, string][] = [];
      if (cameras.some((candidate) => candidate.watch)) {
        const declared = await client.listCameraSettings({}, signal);
        for (const { name, config } of declared.items ?? []) {
          const address = directAddress(config.stream_url);
          if (address) streams.push([name, address]);
        }
      }
      cameraStreams = Object.fromEntries(streams);
      const storedPrinter = found.config.plate_check_printer;
      printerDraft =
        typeof storedPrinter === 'string' && storedPrinter
          ? storedPrinter
          : undefined;
      const storedStream = found.config.plate_camera_stream;
      cameraDraft =
        typeof storedStream === 'string' && storedStream
          ? cameras.find(
              (candidate) =>
                candidate.device_id === printerDraft &&
                Object.hasOwn(cameraStreams, candidate.name) &&
                cameraStreams[candidate.name] === storedStream,
            )?.name
          : undefined;
    } catch (cause) {
      writeError(cause, 'loadFailed');
    } finally {
      loading = false;
    }
  };

  const setCheckEnabled = async (enabled: boolean): Promise<void> => {
    if (!instance || saving || instance.source === 'file') return;
    saving = true;
    error = '';
    saved = false;
    try {
      instance = await writeConfig(instance, { plate_check_enabled: enabled });
      if (enabled && !signal.aborted) await load();
    } catch (cause) {
      writeError(cause, 'toggleFailed');
    } finally {
      saving = false;
    }
  };

  const save = async (): Promise<void> => {
    if (!instance || !printer || !stream || instance.source === 'file') return;
    saving = true;
    error = '';
    saved = false;
    try {
      instance = await writeConfig(instance, {
        plate_check_printer: printer,
        plate_camera_stream: stream,
      });
      saved = true;
    } catch (cause) {
      writeError(cause, 'saveFailed');
    } finally {
      saving = false;
    }
  };

  void load();
</script>

<div class="flex max-w-xl flex-col gap-4">
  {#if instance}
    {@const current = instance}
    <SettingsRow label={t('print-queue.setup.enableCheck')}>
      {#snippet control()}
        <Switch
          aria-label={t('print-queue.setup.enableCheck')}
          checked={current.config.plate_check_enabled !== false}
          disabled={loading ||
            saving ||
            error === 'conflict' ||
            current.source === 'file'}
          onCheckedChange={(enabled) => void setCheckEnabled(enabled)}
        />
      {/snippet}
    </SettingsRow>
  {/if}
  {#if loading}
    <p class="text-sm text-muted-foreground">
      {t('print-queue.setup.loading')}
    </p>
  {:else if instance?.config.plate_check_enabled === false}
    {#if instance.source === 'file'}
      <p class="text-sm text-muted-foreground">
        {t('print-queue.setup.locked')}
      </p>
    {/if}
    {#if typeof instance.config.plate_check_printer === 'string' && instance.config.plate_check_printer}
      {#key instance.config.plate_check_printer}
        <PlateReference
          printer={instance.config.plate_check_printer}
          enabled={false}
        />
      {/key}
    {/if}
  {:else if !fleet.loaded && !fleet.error}
    <p class="text-sm text-muted-foreground">
      {t('print-queue.setup.loading')}
    </p>
  {:else if instance}
    <div class="flex flex-col gap-2">
      <Label for="{uid}-printer">{t('print-queue.setup.printer')}</Label>
      <Select
        value={printer ?? ''}
        items={printers}
        disabled={saving}
        onValueChange={choosePrinter}
      >
        <SelectTrigger id="{uid}-printer" class="w-full">
          <SelectValue placeholder={t('print-queue.setup.pickPrinter')} />
        </SelectTrigger>
        <SelectContent>
          {#each printers as option (option.value)}
            <SelectItem value={option.value} label={option.label}
              >{option.label}</SelectItem
            >
          {/each}
        </SelectContent>
      </Select>
      {#if fleet.error}
        <p class="text-sm text-muted-foreground">
          {t('print-queue.setup.printersFailed')}
        </p>
      {/if}
    </div>

    {#if printer}
      <div class="flex flex-col gap-2">
        <Label for="{uid}-camera">{t('print-queue.setup.camera')}</Label>
        <Select
          value={camera ?? ''}
          items={availableCameras}
          disabled={saving || availableCameras.length === 0}
          onValueChange={(name) => {
            cameraDraft = name;
            previewError = false;
            saved = false;
          }}
        >
          <SelectTrigger id="{uid}-camera" class="w-full">
            <SelectValue placeholder={t('print-queue.setup.pickCamera')} />
          </SelectTrigger>
          <SelectContent>
            {#each availableCameras as option (option.value)}
              <SelectItem value={option.value} label={option.label}
                >{option.label}</SelectItem
              >
            {/each}
          </SelectContent>
        </Select>
        {#if availableCameras.length === 0}
          <p class="text-sm text-muted-foreground">
            {t('print-queue.setup.noCamera')}
          </p>
        {/if}
      </div>
    {/if}

    {#if stream && !configured}
      <p class="text-sm text-muted-foreground">
        {t('print-queue.setup.streamSave')}
      </p>
    {:else if stream && printer}
      <div class="flex flex-col items-start gap-2">
        <img
          class="rounded max-h-64 w-full border object-contain"
          class:hidden={previewError}
          src={`${platePreviewURL(printer)}?tick=${String(previewTick)}`}
          alt={t('print-queue.setup.preview')}
          onerror={() => {
            previewError = true;
          }}
          onload={() => {
            previewError = false;
          }}
        />
        {#if previewError}
          <p class="text-sm text-muted-foreground">
            {t('print-queue.setup.previewFailed')}
          </p>
        {/if}
        <Button
          size="sm"
          variant="secondary"
          onclick={() => {
            previewTick = Date.now();
          }}
        >
          {t('print-queue.setup.refreshPreview')}
        </Button>
      </div>
    {/if}

    {#if instance.source === 'file'}
      <p class="text-sm text-muted-foreground">
        {t('print-queue.setup.locked')}
      </p>
    {:else}
      <div class="flex flex-col items-start gap-2">
        <Button
          disabled={saving ||
            error === 'conflict' ||
            !printer ||
            !stream ||
            configured}
          onclick={save}
        >
          {t('print-queue.setup.useDirectCamera')}
        </Button>
      </div>
    {/if}
    {#if saved}
      <p role="status" class="text-sm">{t('print-queue.setup.saved')}</p>
    {/if}
    {#if printer && configured}
      {#key printer}
        <PlateReference {printer} />
      {/key}
    {/if}
  {/if}
  {#if error}
    <Alert variant="destructive">
      <AlertTitle>{t(`print-queue.setup.${error}`)}</AlertTitle>
    </Alert>
    <div>
      <Button size="sm" variant="secondary" onclick={load}
        >{t('print-queue.setup.reload')}</Button
      >
    </div>
  {/if}
</div>
