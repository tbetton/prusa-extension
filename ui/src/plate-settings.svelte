<script lang="ts">
  import { ApiError, type components, type PrusaConnect } from '@prusa/connect';
  import {
    Alert,
    AlertTitle,
    Button,
    Collapsible,
    CollapsibleContent,
    CollapsibleTrigger,
    Input,
    Label,
    Select,
    SelectContent,
    SelectItem,
    SelectTrigger,
    SelectValue,
  } from '@prusa/connect-ui/primitives';

  import { moduleNamespace } from './api';
  import { heldHost } from './host';

  const host = heldHost();
  const { fleet, signal, t } = host;
  const client = host.client as PrusaConnect;
  const uid = $props.id();
  const defaultOrigin = 'http://127.0.0.1:8080';
  const snapshotPath = (camera: string): string =>
    `/api/v1/cameras/${encodeURIComponent(camera)}/snapshot`;

  type Instance = components['schemas']['ResourceInstance'];
  type Camera = components['schemas']['Camera'];
  type Device = components['schemas']['Device'];

  let instance = $state<Instance>();
  let cameras = $state<Camera[]>([]);
  let printerDraft = $state<string>();
  let cameraDraft = $state<string>();
  let origin = $state(defaultOrigin);
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
      .filter((camera) => camera.device_id === printer && camera.snapshot)
      .map((camera) => ({ value: camera.name, label: camera.name })),
  );
  const camera = $derived(
    availableCameras.find((option) => option.value === cameraDraft)?.value ??
      (availableCameras.length === 1 ? availableCameras[0]?.value : undefined),
  );

  const target = $derived.by(() => {
    if (!camera) return '';
    try {
      const server = new URL(origin.trim());
      if (
        !['http:', 'https:'].includes(server.protocol) ||
        server.username ||
        server.password ||
        server.pathname !== '/' ||
        server.search ||
        server.hash ||
        !['', '80', '443', '8080'].includes(server.port)
      )
        return '';
      return server.origin + snapshotPath(camera);
    } catch {
      return '';
    }
  });

  const choosePrinter = (id: string): void => {
    printerDraft = id;
    cameraDraft = undefined;
    previewError = false;
    saved = false;
  };

  const load = async (): Promise<void> => {
    loading = true;
    error = '';
    saved = false;
    try {
      const [resources, listed] = await Promise.all([
        client.listResources({ query: { kind: 'sink' } }, signal),
        client.listCameras({}, signal),
      ]);
      const instances: Instance[] = resources.items ?? [];
      const found = instances.find(
        (resource) => resource.namespace === moduleNamespace,
      );
      if (!found) {
        error = 'loadFailed';
        return;
      }
      instance = found;
      cameras = listed.items ?? [];
      const storedPrinter = found.config.plate_check_printer;
      printerDraft =
        typeof storedPrinter === 'string' && storedPrinter !== ''
          ? storedPrinter
          : undefined;
      const storedURL = found.config.plate_camera_url;
      cameraDraft = undefined;
      origin = defaultOrigin;
      if (typeof storedURL === 'string' && URL.canParse(storedURL)) {
        const url = new URL(storedURL);
        origin = url.origin;
        cameraDraft = cameras.find(
          (candidate) =>
            candidate.device_id === printerDraft &&
            snapshotPath(candidate.name) === url.pathname,
        )?.name;
      }
    } catch {
      if (!signal.aborted) error = 'loadFailed';
    } finally {
      loading = false;
    }
  };

  const save = async (): Promise<void> => {
    if (!instance || !printer || !target || instance.source === 'file') return;
    saving = true;
    error = '';
    saved = false;
    const current = instance;
    try {
      instance = await client.putResource(
        {
          path: { kind: 'sink', name: current.name },
          header: { 'If-Match': `"${current.version}"` },
          body: {
            type: current.type,
            enabled: current.enabled,
            required: current.required,
            config: {
              ...current.config,
              plate_check_printer: printer,
              plate_camera_url: target,
            },
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
      saved = true;
    } catch (cause) {
      if (signal.aborted) return;
      error = 'saveFailed';
      if (cause instanceof ApiError && cause.status === 412) error = 'conflict';
      if (cause instanceof ApiError && cause.status === 403)
        error = 'forbidden';
    } finally {
      saving = false;
    }
  };

  void load();
</script>

<div class="flex max-w-xl flex-col gap-4">
  {#if loading || (!fleet.loaded && !fleet.error)}
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

    {#if camera}
      <div class="flex flex-col items-start gap-2">
        <img
          class="rounded max-h-64 w-full border object-contain"
          class:hidden={previewError}
          src={`${snapshotPath(camera)}?tick=${String(previewTick)}`}
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

    <Collapsible class="text-sm">
      <CollapsibleTrigger variant="inline"
        >{t('print-queue.setup.connection')}</CollapsibleTrigger
      >
      <CollapsibleContent class="mt-2 flex flex-col gap-2">
        <Label for="{uid}-origin">{t('print-queue.setup.origin')}</Label>
        <Input
          id="{uid}-origin"
          type="url"
          value={origin}
          disabled={saving}
          oninput={(event) => {
            origin = event.currentTarget.value;
            saved = false;
          }}
        />
        <p class="text-muted-foreground">{t('print-queue.setup.originHint')}</p>
        {#if camera && !target}
          <p class="text-warning">{t('print-queue.setup.invalidOrigin')}</p>
        {/if}
      </CollapsibleContent>
    </Collapsible>

    {#if instance.source === 'file'}
      <p class="text-sm text-muted-foreground">
        {t('print-queue.setup.locked')}
      </p>
    {:else}
      <div>
        <Button
          disabled={saving ||
            error === 'conflict' ||
            !printer ||
            !target ||
            (printer === instance.config.plate_check_printer &&
              target === instance.config.plate_camera_url)}
          onclick={save}
        >
          {t('print-queue.save')}
        </Button>
      </div>
    {/if}
    {#if saved}
      <p role="status" class="text-sm">{t('print-queue.setup.saved')}</p>
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
