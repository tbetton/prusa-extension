<!--
  The PrusaSlicer pane on this module's page under Settings, Extensions: how
  to send a file from PrusaSlicer 2 to a queue, through the PrusaSlicer 2
  Connector extension. A settings pane takes no props, and the host draws it
  with no heading, so the pane names itself.
-->
<script lang="ts">
  import { SOURCE_LOCALE } from '@prusa/connect-ui/i18n';
  import { SettingsPane } from '@prusa/connect-ui/primitives';

  import { heldHost } from './host';

  const t = heldHost().t;

  const steps = [
    'print-queue.howTo.steps.install',
    'print-queue.howTo.steps.card',
    'print-queue.howTo.steps.slicer',
    'print-queue.howTo.steps.save',
    'print-queue.howTo.steps.queue',
    'print-queue.howTo.steps.add',
  ];
</script>

<SettingsPane title={t('print-queue.howTo.title')} edged>
  <div
    lang={SOURCE_LOCALE}
    class="grid max-w-3xl gap-section-gap p-panel-padding-mobile @lg/pane:p-panel-padding"
    data-part="how-to"
  >
    <p class="text-body">{t('print-queue.howTo.intro')}</p>
    <p class="text-body font-medium">{t('print-queue.howTo.stepsTitle')}</p>
    <ol class="text-body grid list-decimal gap-2 ps-5 text-muted-foreground">
      {#each steps as step (step)}
        <li>{t(step)}</li>
      {/each}
    </ol>
    <p class="text-body text-muted-foreground">{t('print-queue.howTo.limits')}</p>
  </div>
</SettingsPane>
