<script lang="ts">
  import { onMount } from 'svelte';
  import {
    Alert,
    AlertDescription,
    AlertTitle,
    Badge,
    Button,
    SettingsRow,
    Switch,
  } from '@prusa/connect-ui/primitives';

  import { listJobs, updatePlateCheck, type PlateCheck } from './api';
  import { heldHost } from './host';
  import { formatDate, queueErrorText } from './i18n';

  let { printer, enabled = true }: { printer: string; enabled?: boolean } =
    $props();

  const heading = $props.id();
  const host = heldHost();
  const { t } = host;
  const controller = new AbortController();
  const signal = AbortSignal.any([host.signal, controller.signal]);

  let status = $state<PlateCheck>();
  let readError = $state(false);
  let actionError = $state('');
  let busy = $state(false);
  let request = 0;

  const refresh = async (): Promise<void> => {
    const id = ++request;
    try {
      const queue = await listJobs(printer, signal);
      if (signal.aborted || id !== request) return;
      status = queue.plate_checks?.[printer];
      readError = false;
    } catch {
      if (!signal.aborted && id === request) readError = true;
    }
  };

  const update = async (
    change: Parameters<typeof updatePlateCheck>[1],
  ): Promise<void> => {
    busy = true;
    actionError = '';
    try {
      await updatePlateCheck(printer, change, signal);
      await refresh();
    } catch (cause) {
      if (!signal.aborted) actionError = queueErrorText(cause, t);
    } finally {
      busy = false;
    }
  };

  onMount(() => {
    void refresh();
    const timer = setInterval(() => {
      if (!signal.aborted && !busy) void refresh();
    }, 5000);
    return () => {
      clearInterval(timer);
      controller.abort();
    };
  });

  const timeLabel = (at: string): string =>
    formatDate(at, { dateStyle: 'short', timeStyle: 'short' });
</script>

{#if enabled || status?.status === 'readyBlocked'}
  <section class="flex flex-col gap-3 border-t pt-4" aria-labelledby={heading}>
    <div class="flex flex-wrap items-center gap-2">
      <h3 id={heading} class="font-medium">
        {t('print-queue.plate.referenceTitle')}
      </h3>
      {#if status}
        <Badge variant="outline"
          >{t(`print-queue.plate.status.${status.status}`)}</Badge
        >
      {/if}
    </div>
    {#if readError}
      <p class="text-sm text-muted-foreground">
        {t('print-queue.plate.statusUnavailable')}
      </p>
    {:else if !status}
      <p class="text-sm text-muted-foreground">
        {t('print-queue.plate.waitingForSettings')}
      </p>
    {/if}
    {#if status}
      {@const plate = status}
      {@const calibrating = plate.calibration && !plate.calibration.failed}
      <div class="flex flex-wrap gap-x-3 gap-y-1 text-sm text-muted-foreground">
        {#if plate.reference_at}
          <span
            >{t('print-queue.plate.referenceAt', {
              at: timeLabel(plate.reference_at),
            })}</span
          >
        {/if}
        {#if plate.profile_count}
          <span
            >{t('print-queue.plate.profiles', {
              count: plate.profile_count,
            })}</span
          >
        {/if}
        {#if plate.last_match_at}
          <span
            >{t('print-queue.plate.lastMatchAt', {
              at: timeLabel(plate.last_match_at),
            })}</span
          >
        {/if}
      </div>
      {#if plate.status === 'readyBlocked'}
        <Alert variant="destructive">
          <AlertTitle>{t('print-queue.plate.status.readyBlocked')}</AlertTitle>
          <AlertDescription>{t('print-queue.plate.recover')}</AlertDescription>
        </Alert>
      {/if}
      {#if enabled && plate.can_configure}
        <div class="flex flex-col items-start gap-2">
          <p class="text-sm text-muted-foreground">
            {t('print-queue.plate.captureHint')}
          </p>
          {#if plate.calibration?.failed}
            <p role="status" class="text-sm">
              {t('print-queue.plate.calibrationFailed')}
            </p>
          {/if}
          {#if plate.calibration?.issue}
            <p class="text-sm text-muted-foreground">
              {t(`print-queue.plate.calibrationIssue.${plate.calibration.issue}`)}
            </p>
          {/if}
          {#if calibrating && plate.calibration}
            <p role="status" class="text-sm">
              {t('print-queue.plate.calibrationProgress', {
                count: plate.calibration.captured,
                total: plate.calibration.target,
              })}
            </p>
            <Button
              size="sm"
              variant="secondary"
              disabled={busy || readError}
              onclick={() => void update({ action: 'cancel_calibration' })}
            >
              {t('print-queue.plate.calibrationCancel')}
            </Button>
          {:else}
            <Button
              size="sm"
              variant="secondary"
              disabled={busy || readError}
              onclick={() => void update({ action: 'start_calibration' })}
            >
              {t('print-queue.plate.capture')}
            </Button>
          {/if}
        </div>
        <SettingsRow
          label={t('print-queue.plate.automatic')}
          description={t('print-queue.plate.limit')}
        >
          {#snippet control()}
            <Switch
              aria-label={t('print-queue.plate.automatic')}
              checked={plate.automatic}
              disabled={busy ||
                readError ||
                !!calibrating ||
                !plate.reference_at}
              onCheckedChange={(automatic) =>
                void update({ action: 'set_automatic', automatic })}
            />
          {/snippet}
        </SettingsRow>
      {/if}
    {/if}
    {#if actionError}
      <Alert variant="destructive">
        <AlertTitle>{actionError}</AlertTitle>
      </Alert>
    {/if}
  </section>
{/if}
