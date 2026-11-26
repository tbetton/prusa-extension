<!-- The Print Queue tab of a printer's page: the same view as the page, for one printer. -->
<script lang="ts">
  import QueueView from './queue-view.svelte';

  interface Props {
    /** The device this tab is about. It is the only prop a tab receives. */
    deviceId: string;
  }

  let { deviceId }: Props = $props();
</script>

<QueueView {deviceId} />
