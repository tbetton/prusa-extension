<!-- The Print Queue page of the sidebar: the queue of every printer. -->
<script lang="ts">
  import { SOURCE_LOCALE } from '@prusa/connect-ui/i18n';
  import { PageHeader } from '@prusa/connect-ui/primitives';

  import { heldHost } from './host';
  import QueueView from './queue-view.svelte';

  const t = heldHost().t;
</script>

<div lang={SOURCE_LOCALE} class="flex flex-col gap-4">
  <PageHeader title={t('print-queue.page.title')} />
  <QueueView />
</div>
