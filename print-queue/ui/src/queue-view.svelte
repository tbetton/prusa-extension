<script lang="ts">
  import { onDestroy } from 'svelte';
  import { isPrinterState } from '@prusa/connect-ui/domain';
  import { SOURCE_LOCALE } from '@prusa/connect-ui/i18n';
  import {
    Badge,
    Button,
    Card,
    CardContent,
    CardHeader,
    CardTitle,
    ConfirmDialog,
    EmptyState,
    Tabs,
    TabsContent,
    TabsList,
    TabsTrigger,
    toast,
  } from '@prusa/connect-ui/primitives';

  import {
    clearFinished,
    listHistory,
    listJobs,
    markPrinterReady,
    moveJob,
    removeJob,
    requeueJob,
    scheduleJob,
    setPaused,
    thumbnailUrl,
    type Job,
    type Queue,
  } from './api';
  import { heldHost } from './host';
  import { formatClock, formatDate, formatDuration, formatNumber, queueErrorText } from './i18n';
  import PrinterFiles from './printer-files.svelte';
  import {
    CHANGEOVER_MS,
    correction,
    includeCurrentPrint,
    schedule,
    type CurrentPrint,
    type PlanJob,
    type Slot,
  } from './plan';
  import Timeline from './timeline.svelte';

  interface Props {
    /** One printer, or every printer when absent. */
    deviceId?: string;
  }

  let { deviceId }: Props = $props();

  const host = heldHost();
  const t = host.t;
  const fleet = host.fleet;

  let queue = $state<Queue | undefined>(undefined);
  let failure = $state<{ error: unknown } | undefined>(undefined);
  let editing = $state<string | undefined>(undefined);
  let editValue = $state('');
  let busy = $state(false);
  let picking = $state<string | undefined>(undefined);
  let markingReady = $state<string | undefined>(undefined);
  let tab = $state('active');
  let now = $state(Date.now());
  /** Real / slicer duration ratio, per printer. */
  let factors = $state<Record<string, number>>({});

  let currentPrints = $state<Record<string, CurrentPrint>>({});
  let refreshingPrints = false;

  const refreshCurrentPrints = async (): Promise<void> => {
    if (refreshingPrints || host.signal.aborted) return;
    refreshingPrints = true;
    try {
      const candidates = fleet.devices.filter((device) => {
        if (deviceId !== undefined && device.id !== deviceId) return false;
        if (device.kind !== 'printer' && device.kind !== '') return false;
        const state = device.state['/state'];
        return device.job?.running || state === 'PRINTING' || state === 'PAUSED' ||
          state === 'ATTENTION' || state === 'BUSY' || currentPrints[device.id] !== undefined;
      });
      const entries = await Promise.all(candidates.map(async (device) => {
        try {
          const { current } = await host.client.getDeviceJobs({ path: { device_id: device.id } }, host.signal);
          return [device.id, current?.ended === undefined ? current : undefined] as const;
        } catch {
          return [device.id, undefined] as const;
        }
      }));
      currentPrints = Object.fromEntries(entries.filter((entry): entry is readonly [string, CurrentPrint] => entry[1] !== undefined));
    } finally {
      refreshingPrints = false;
    }
  };

  const refresh = async (): Promise<void> => {
    try {
      queue = await listJobs(deviceId, host.signal);
      failure = undefined;
    } catch (error) {
      if (host.signal.aborted) return;
      failure = { error };
    }
  };

  const loadFactors = async (): Promise<void> => {
    for (const id of printers) {
      try {
        factors[id] = correction(await listHistory(id, host.signal));
      } catch {
        // keep the last one
      }
    }
  };

  void refresh();
  void refreshCurrentPrints();
  const timer = setInterval(() => {
    now = Date.now();
    void refresh();
    void refreshCurrentPrints();
  }, 5000);
  const slow = setInterval(() => void loadFactors(), 10 * 60_000);
  host.signal.addEventListener('abort', () => { clearInterval(timer); clearInterval(slow); }, { once: true });
  onDestroy(() => {
    clearInterval(timer);
    clearInterval(slow);
  });

  const act = async (work: () => Promise<unknown>): Promise<void> => {
    busy = true;
    try {
      await work();
      await refresh();
    } catch (error) {
      toast.error(queueErrorText(error, t));
    } finally {
      busy = false;
    }
  };

  /** The printers to draw: the one asked for, or every one with a job. */
  const printers = $derived.by(() => {
    if (deviceId !== undefined) return [deviceId];
    const ids = fleet.devices
      .filter((device) => device.kind === 'printer' || device.kind === '')
      .map((device) => device.id);
    for (const job of queue?.jobs ?? []) {
      if (!ids.includes(job.device_id)) ids.push(job.device_id);
    }
    return ids;
  });

  const jobsOf = (id: string): Job[] => (queue?.jobs ?? []).filter((job) => job.device_id === id);

  const nameOf = (id: string): string => fleet.device(id)?.name ?? id;

  const manualReadiness = (id: string): 'available' | 'printing' | 'offline' | 'ready' | 'unavailable' | undefined => {
    const device = fleet.device(id);
    if (device?.actions?.includes('operate') !== true) return undefined;
    if (!device.connected) return 'offline';
    const state = device.state['/state'];
    if (device.job?.running || currentPrints[id] || state === 'PRINTING' || state === 'PAUSED' ||
      jobsOf(id).some((job) => job.status === 'starting' || job.status === 'printing')) {
      return 'printing';
    }
    if (state === 'READY') return queue?.ready_confirmation_required?.[id] ? 'available' : 'ready';
    if (device.capabilities?.includes('core.command.set_printer_ready') !== true ||
      (state !== 'IDLE' && state !== 'FINISHED' && state !== 'STOPPED')) {
      return 'unavailable';
    }
    return 'available';
  };

  const stateOf = (id: string): string => {
    const state = fleet.device(id)?.state as Record<string, unknown> | undefined;
    const value = state?.['/state'];
    if (typeof value !== 'string') return '';
    return isPrinterState(value) ? t(`print-queue.state.${value}`) : value;
  };

  const variant = (status: Job['status'] | 'unconfirmed') => {
    switch (status) {
      case 'printing':
      case 'starting':
        return 'info' as const;
      case 'done':
        return 'success' as const;
      case 'failed':
        return 'destructive' as const;
      case 'stopped':
      case 'unconfirmed':
        return 'warning' as const;
      default:
        return 'secondary' as const;
    }
  };

  const when = (iso: string | undefined): string =>
    iso
      ? formatDate(iso, {
          weekday: 'short',
          day: '2-digit',
          month: '2-digit',
          hour: '2-digit',
          minute: '2-digit',
        })
      : '';

  /** A Date as the value a datetime-local input reads, in local time. */
  const localValue = (date: Date): string => {
    const pad = (n: number) => String(n).padStart(2, '0');
    return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}`;
  };

  const startEditing = (job: PlanJob): void => {
    editing = job.id;
    const base = job.not_before ? new Date(job.not_before) : new Date(Date.now() + 3600_000);
    editValue = localValue(base);
  };

  const saveSchedule = (job: PlanJob): void => {
    const at = editValue === '' ? null : new Date(editValue);
    editing = undefined;
    void act(() => scheduleJob(job.id, at));
  };

  const FINISHED: Job['status'][] = ['done', 'stopped', 'failed'];
  const isFinished = (job: PlanJob): boolean => FINISHED.includes(job.status);
  const isRunning = (job: Pick<Job, 'status'>): boolean => job.status === 'printing' || job.status === 'starting';
  const ms = (iso: string | undefined): number => (iso ? new Date(iso).getTime() : 0);

  /**
   * The order the printer will work through: what is running, then the
   * queued jobs that may start now, in queue order, then the ones that wait
   * for a time, soonest first (the ones behind them may start before them).
   */
  const activeOf = (id: string): PlanJob[] => {
    const jobs = jobsOf(id);
    const running = includeCurrentPrint(
      jobs.filter(isRunning),
      currentPrints[id],
      t('print-queue.currentPrint'),
    );
    const queued = jobs.filter((job) => job.status === 'queued');
    const ready = queued.filter((job) => ms(job.not_before) <= now);
    const later = queued
      .filter((job) => ms(job.not_before) > now)
      .sort((a, b) => ms(a.not_before) - ms(b.not_before));
    return [...running, ...ready, ...later];
  };

  /** Finished jobs, the most recent first. */
  const finishedOf = (id: string): Job[] =>
    jobsOf(id)
      .filter(isFinished)
      .sort((a, b) => ms(b.ended_at ?? b.started_at) - ms(a.ended_at ?? a.started_at));

  $effect(() => {
    if (printers.length > 0 && Object.keys(factors).length === 0) void loadFactors();
  });

  const slotsOf = (id: string): Slot[] => {
    const jobs = activeOf(id);
    const current = currentPrints[id];
    const queueId = jobs[0]?.id;
    if (current === undefined || queueId === undefined) {
      return schedule(jobs, now, factors[id] ?? 1);
    }

    const reading = fleet.device(id)?.job;
    const live = reading?.running && (reading.id ? reading.id === current.id : reading.path === current.path)
      ? reading
      : current;
    return schedule(jobs, now, factors[id] ?? 1, {
      queueId,
      remainingMs: live.time_remaining_ms > 0 ? live.time_remaining_ms : undefined,
      progress: live.progress,
    });
  };

  /** Printer paths already waiting or printing, with how many times. */
  const queuedPaths = (id: string): Record<string, number> => {
    const counts: Record<string, number> = {};
    for (const job of jobsOf(id)) {
      if (job.storage === '' && !isFinished(job)) counts[job.path] = (counts[job.path] ?? 0) + 1;
    }
    return counts;
  };

  const jobDetail = (job: PlanJob): string | undefined => {
    if (!job.detail_code) return job.detail;
    const key = `print-queue.issue.${job.detail_code}`;
    const message = t(key);
    return message === key ? job.detail : message;
  };

  const activeCount = $derived(printers.reduce((n, id) => n + activeOf(id).length, 0));
  const finishedCount = $derived(printers.reduce((n, id) => n + finishedOf(id).length, 0));
</script>

{#snippet jobRow(job: PlanJob, position?: number, slot?: Slot)}
  {@const detail = jobDetail(job)}
  {@const displayStatus = job.detail_code === 'unconfirmed' ? 'unconfirmed' : job.status}
  <li class="flex items-start gap-3 py-3">
    {#if job.path}
      <img
        src={thumbnailUrl(job.device_id, job.storage, job.path)}
        alt=""
        loading="lazy"
        class="size-12 shrink-0 rounded bg-muted object-contain sm:size-16"
        onerror={(event) => {
          if (event.currentTarget instanceof HTMLImageElement) {
            event.currentTarget.style.visibility = 'hidden';
          }
        }}
      />
    {/if}
    <div class="grid min-w-0 flex-1 grid-cols-1 gap-x-4 gap-y-2 lg:grid-cols-[minmax(0,1fr)_auto]">
      <div class="min-w-0">
        <div class="flex flex-wrap items-center gap-x-2 gap-y-1">
          <Badge variant={variant(displayStatus)}>{t(`print-queue.status.${displayStatus}`)}</Badge>
          {#if position !== undefined}
            <span class="text-sm tabular-nums text-muted-foreground">{position}.</span>
          {/if}
          <span class="min-w-0 flex-1 wrap-anywhere font-medium" title={job.path}>{job.name}</span>
        </div>
        <div class="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-xs text-muted-foreground">
          {#if slot?.progress !== undefined}
            <span class="tabular-nums text-foreground">
              {formatNumber(slot.progress / 100, { style: 'percent', maximumFractionDigits: 0 })}
            </span>
          {/if}
          {#if slot?.duration !== undefined}
            <span class="text-foreground">
              {formatDuration(slot.running ? slot.end - slot.start : slot.duration)}
              {#if slot.running}
                · {t('print-queue.plan.endsAt')} {formatClock(slot.end, now)}
                ({t('print-queue.plan.left')} {formatDuration(slot.end - now)})
              {:else}
                · {formatClock(slot.start, now)} → {formatClock(slot.end, now)}
              {/if}
            </span>
          {:else if slot !== undefined}
            <span>{t('print-queue.plan.unknown')}</span>
          {/if}
          {#if job.not_before && job.status === 'queued'}
            <span>{t('print-queue.notBefore')} {when(job.not_before)}</span>
          {/if}
          {#if job.started_at}
            <span>{t('print-queue.startedAt')} {when(job.started_at)}</span>
          {/if}
          {#if job.ended_at}
            <span>{t('print-queue.endedAt')} {when(job.ended_at)}</span>
          {/if}
        </div>
        {#if job.path}
          <p class="mt-1 truncate text-xs text-muted-foreground" title={job.path}>
            {job.storage ? `${job.storage}:${job.path}` : `${t('print-queue.onPrinter')} ${job.path}`}
          </p>
        {/if}
        {#if detail}
          <p class="mt-1 text-xs text-destructive">{detail}</p>
        {/if}
        {#if editing === job.id}
          <div class="mt-2 flex flex-wrap items-center gap-2">
            <input
              type="datetime-local"
              lang={SOURCE_LOCALE}
              aria-label={t('print-queue.notBefore')}
              class="h-8 rounded-md border border-input bg-background px-2 text-sm"
              bind:value={editValue}
            />
            <Button size="sm" disabled={busy} onclick={() => saveSchedule(job)}>{t('print-queue.save')}</Button>
            {#if job.not_before}
              <Button size="sm" variant="secondary" disabled={busy} onclick={() => { editValue = ''; saveSchedule(job); }}>
                {t('print-queue.asap')}
              </Button>
            {/if}
            <Button size="sm" variant="ghost" onclick={() => (editing = undefined)}>{t('print-queue.cancel')}</Button>
          </div>
        {/if}
      </div>
      {#if !isRunning(job)}
        <div class="flex flex-wrap items-start gap-1 lg:justify-end">
          {#if job.status === 'queued'}
            <Button size="sm" variant="ghost" aria-label={t('print-queue.up')} disabled={busy} onclick={() => act(() => moveJob(job.id, 'up'))}>↑</Button>
            <Button size="sm" variant="ghost" aria-label={t('print-queue.down')} disabled={busy} onclick={() => act(() => moveJob(job.id, 'down'))}>↓</Button>
            <Button size="sm" variant="ghost" disabled={busy} onclick={() => startEditing(job)}>
              {t('print-queue.schedule')}
            </Button>
          {/if}
          {#if isFinished(job)}
            <Button size="sm" variant="ghost" disabled={busy} onclick={() => act(() => requeueJob(job.id))}>
              {t('print-queue.requeue')}
            </Button>
          {/if}
          <Button size="sm" variant="ghost" disabled={busy} onclick={() => act(() => removeJob(job.id))}>
            {t('print-queue.remove')}
          </Button>
        </div>
      {/if}
    </div>
  </li>
{/snippet}


<div lang={SOURCE_LOCALE} class="flex flex-col gap-4">
  {#if failure !== undefined}
    <p class="text-sm text-destructive">{t('print-queue.error', { message: queueErrorText(failure.error, t) })}</p>
  {/if}

  {#if queue?.problem}
    <p class="text-sm text-warning">{queue.problem}</p>
  {/if}

  {#if queue !== undefined && printers.length === 0}
    <EmptyState size="sm" heading={t('print-queue.empty.title')} description={t('print-queue.empty.body')} />
  {/if}

  <Tabs bind:value={tab}>
    <TabsList aria-label={t('print-queue.page.title')}>
      <TabsTrigger value="active">{t('print-queue.tab.active')} ({formatNumber(activeCount)})</TabsTrigger>
      <TabsTrigger value="done">{t('print-queue.tab.done')} ({formatNumber(finishedCount)})</TabsTrigger>
    </TabsList>

    <TabsContent value="active" class="flex flex-col gap-4 pt-4">
      {#each printers as printer (printer)}
        {@const slots = slotsOf(printer)}
        {@const jobs = slots.map((slot) => slot.job)}
        {@const paused = queue?.paused[printer] === true}
        {@const factor = factors[printer] ?? 1}
        {@const readiness = manualReadiness(printer)}
        {@const readinessNotice = readiness && readiness !== 'available' && readiness !== 'printing' ? readiness : undefined}
        <Card class="min-w-0">
          <CardHeader>
            <div class="flex flex-wrap items-center justify-between gap-x-4 gap-y-3">
              <div class="flex min-w-0 flex-wrap items-center gap-2">
                {#if deviceId === undefined}
                  <CardTitle>{nameOf(printer)}</CardTitle>
                {/if}
                {#if stateOf(printer)}
                  <Badge variant="outline">{stateOf(printer)}</Badge>
                {/if}
                {#if paused}
                  <Badge variant="warning">{t('print-queue.queueHeld')}</Badge>
                {/if}
              </div>
              <div class="flex flex-wrap gap-2">
                <Button size="sm" onclick={() => (picking = printer)}>
                  + {t('print-queue.pick.open')}
                </Button>
                {#if readiness !== undefined}
                  <Button
                    size="sm"
                    variant="secondary"
                    disabled={busy || readiness !== 'available'}
                    aria-describedby={readinessNotice ? `ready-status-${printer}` : undefined}
                    onclick={() => (markingReady = printer)}
                  >
                    {t('print-queue.markReady.button')}
                  </Button>
                {/if}
                <Button
                  size="sm"
                  variant={paused ? 'default' : 'secondary'}
                  disabled={busy}
                  onclick={() => act(() => setPaused(printer, !paused))}
                >
                  {paused ? t('print-queue.resume') : t('print-queue.pause')}
                </Button>
              </div>
            </div>
            {#if readinessNotice}
              <p class="text-xs text-muted-foreground" id={`ready-status-${printer}`}>
                {t(`print-queue.markReady.status.${readinessNotice}`)}
              </p>
            {/if}
            {#if queue?.ready_confirmation_required?.[printer]}
              <p class="text-sm text-warning">{t('print-queue.markReady.confirmRequired')}</p>
            {/if}
            {#if paused}
              <p class="text-sm text-warning">{t('print-queue.pausedHint')}</p>
            {/if}
          </CardHeader>
          <CardContent>
            {#if jobs.length > 0}
              <div class="mb-3 flex flex-col gap-1">
                <Timeline {slots} {now} />
                <p class="text-xs text-muted-foreground">
                  {t('print-queue.plan.note', {
                    changeover: formatNumber(Math.round(CHANGEOVER_MS / 60_000)),
                    factor: formatNumber(factor, { maximumFractionDigits: 2 }),
                  })}
                </p>
              </div>
            {/if}
            {#if jobs.length === 0}
              <p class="text-sm text-muted-foreground">{t('print-queue.noJobs')}</p>
            {:else}
              {@const firstQueued = jobs.findIndex((job) => job.status === 'queued')}
              <ol class="flex flex-col divide-y divide-border">
                {#each slots as slot, index (slot.job.id)}
                  {@render jobRow(slot.job, slot.job.status === 'queued' ? index - firstQueued + 1 : undefined, slot)}
                {/each}
              </ol>
            {/if}
          </CardContent>
        </Card>
      {/each}
      <p class="text-xs text-muted-foreground">{t('print-queue.howToAdd')}</p>
    </TabsContent>

    <TabsContent value="done" class="flex flex-col gap-4 pt-4">
      {#each printers as printer (printer)}
        {@const jobs = finishedOf(printer)}
        <Card>
          <CardHeader>
            <div class="flex flex-wrap items-center justify-between gap-2">
              <CardTitle>
                {deviceId === undefined ? nameOf(printer) : t('print-queue.tab.done')}
              </CardTitle>
              {#if jobs.length > 0}
                <Button size="sm" variant="ghost" disabled={busy} onclick={() => act(() => clearFinished(printer))}>
                  {t('print-queue.clearFinished')}
                </Button>
              {/if}
            </div>
          </CardHeader>
          <CardContent>
            {#if jobs.length === 0}
              <p class="text-sm text-muted-foreground">{t('print-queue.noFinished')}</p>
            {:else}
              <ol class="flex flex-col divide-y divide-border">
                {#each jobs as job (job.id)}
                  {@render jobRow(job)}
                {/each}
              </ol>
            {/if}
          </CardContent>
        </Card>
      {/each}
    </TabsContent>
  </Tabs>

  {#if picking !== undefined}
    {#key picking}
      <PrinterFiles
        deviceId={picking}
        printerName={nameOf(picking)}
        queued={queuedPaths(picking)}
        onadded={() => void refresh()}
        onclose={() => (picking = undefined)}
      />
    {/key}
  {/if}
  <ConfirmDialog
    open={markingReady !== undefined}
    onOpenChange={(open) => {
      if (!open) markingReady = undefined;
    }}
    variant="confirm"
    title={t('print-queue.markReady.title', { printer: markingReady ? nameOf(markingReady) : '' })}
    description={t('print-queue.markReady.confirm')}
    confirmLabel={t('print-queue.markReady.button')}
    confirmDisabled={busy || markingReady === undefined || manualReadiness(markingReady) !== 'available'}
    onConfirm={() => {
      const printer = markingReady;
      markingReady = undefined;
      if (printer !== undefined && manualReadiness(printer) === 'available') {
        void act(() => markPrinterReady(printer));
      }
    }}
  />
</div>
