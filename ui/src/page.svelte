<script lang="ts">
  import { PageHeader } from '@prusa/connect-ui/primitives';

  import { heldHost } from './host';
  import QueueView from './queue-view.svelte';

  const t = heldHost().t;
</script>

<div class="flex flex-col gap-4">
  <PageHeader title={t('print-queue.page.title')} />
  <QueueView />
</div>
