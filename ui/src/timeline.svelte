<!--
  The printer's expected activity on a time ruler, like Prusa Connect's own
  queue: a fixed scale that scrolls sideways, the hours marked, a red line for
  now and one block per print at its expected time.

  « Faire défiler jusqu'à maintenant » brings now back into view. With the
  automatic scroll on, the view follows now as time passes; scrolling by hand
  turns it off.
-->
<script lang="ts">
  import { formatDate } from '@prusa/connect-ui/i18n';

  import { formatClock, formatDuration } from './i18n';
  import type { Slot } from './plan';
  import { heldHost } from './host';

  interface Props {
    slots: Slot[];
    now: number;
  }

  let { slots, now }: Props = $props();

  const t = heldHost().t;

  const HOUR = 3600_000;
  /** Width of one hour on the ruler, in pixels. */
  const PX_PER_HOUR = 34;
  /** How much of the past stays to the left of now when scrolled to it. */
  const LEAD = 1.5 * HOUR;

  let scroller = $state<HTMLDivElement | undefined>(undefined);
  let follow = $state(true);
  let programmatic = false;

  const dated = $derived(slots.filter((slot) => slot.duration !== undefined));

  /** From two hours before the earliest block (or now) to a day past the last one. */
  const from = $derived.by(() => {
    const earliest = Math.min(now, ...dated.map((slot) => slot.start)) - 2 * HOUR;
    const start = new Date(earliest);
    start.setMinutes(0, 0, 0);
    return start.getTime();
  });
  const to = $derived(Math.max(now + 36 * HOUR, ...dated.map((slot) => slot.end + 12 * HOUR)));

  const x = (ms: number): number => ((ms - from) / HOUR) * PX_PER_HOUR;
  const width = $derived(x(to));

  /** Every hour from the first whole hour; labels every six. */
  const hours = $derived.by(() => {
    const marks: { at: number; label?: string; day?: string }[] = [];
    for (let at = from; at <= to; at += HOUR) {
      const date = new Date(at);
      const h = date.getHours();
      if (h % 6 === 0) {
        marks.push({
          at,
          label: formatDate(at, { hour: 'numeric', minute: '2-digit' }),
          ...(h === 0
            ? { day: formatDate(at, { weekday: 'short', day: 'numeric' }) }
            : {}),
        });
      } else {
        marks.push({ at });
      }
    }
    return marks;
  });

  const scrollToNow = (): void => {
    if (!scroller) return;
    programmatic = true;
    scroller.scrollTo({ left: Math.max(0, x(now - LEAD)), behavior: 'smooth' });
    setTimeout(() => (programmatic = false), 600);
  };

  // Follow now while the automatic scroll is on.
  $effect(() => {
    void now;
    void from;
    if (follow && scroller) scrollToNow();
  });

  const onScroll = (): void => {
    if (!programmatic) follow = false;
  };

  // No scrollbar: the ruler moves by dragging it, or sideways with a trackpad.
  let dragFrom: { x: number; left: number } | undefined;
  const onPointerDown = (event: PointerEvent): void => {
    if (!scroller || event.button !== 0) return;
    dragFrom = { x: event.clientX, left: scroller.scrollLeft };
    scroller.setPointerCapture(event.pointerId);
  };
  const onPointerMove = (event: PointerEvent): void => {
    if (!scroller || !dragFrom) return;
    scroller.scrollLeft = dragFrom.left - (event.clientX - dragFrom.x);
  };
  const onPointerUp = (event: PointerEvent): void => {
    dragFrom = undefined;
    scroller?.releasePointerCapture(event.pointerId);
  };

  const tone = (slot: Slot): string =>
    slot.running
      ? 'bg-primary/70 text-white'
      : slot.job.not_before && new Date(slot.job.not_before).getTime() > now
        ? 'bg-warning/60 text-white'
        : 'bg-primary/35 text-white';

  const tip = (slot: Slot): string =>
    `${slot.job.name.replace(/\+/gu, ' ')}\n${formatClock(slot.start, now)} → ${formatClock(slot.end, now)} (${formatDuration(slot.end - slot.start)})`;
</script>

{#if dated.length > 0}
  <div class="flex flex-col gap-1">
    <div class="flex justify-end gap-4 text-xs uppercase tracking-wide">
      <button class="underline underline-offset-2 hover:text-primary" onclick={() => { follow = true; scrollToNow(); }}>
        {t('print-queue.plan.scrollNow')}
      </button>
      <button class="text-muted-foreground hover:text-foreground" onclick={() => { follow = !follow; if (follow) scrollToNow(); }}>
        {follow ? t('print-queue.plan.followOn') : t('print-queue.plan.followOff')}
      </button>
    </div>

    <div
      bind:this={scroller}
      class="cursor-grab overflow-x-auto border-y border-border select-none active:cursor-grabbing [scrollbar-width:none] [&::-webkit-scrollbar]:hidden"
      onscroll={onScroll}
      onpointerdown={onPointerDown}
      onpointermove={onPointerMove}
      onpointerup={onPointerUp}
      onpointercancel={onPointerUp}
      role="presentation"
    >
      <div class="relative" style:width="{width}px">
        <!-- ruler -->
        <div class="relative h-10 border-b border-border">
          {#each hours as mark (mark.at)}
            <div
              class={['absolute bottom-0 w-px bg-muted-foreground/50', mark.label ? 'h-3' : 'h-1.5']}
              style:left="{x(mark.at)}px"
            ></div>
            {#if mark.label}
              <span class="absolute top-1 -translate-x-1/2 text-xs whitespace-nowrap" style:left="{x(mark.at)}px">
                {mark.label}
              </span>
              {#if mark.day}
                <span
                  class="absolute top-1 ml-6 text-[10px] whitespace-nowrap text-muted-foreground"
                  style:left="{x(mark.at)}px"
                >
                  {mark.day}
                </span>
              {/if}
            {/if}
          {/each}
        </div>

        <!-- blocks -->
        <div class="relative h-14">
          {#each dated as slot (slot.job.id)}
            <div
              class={['absolute top-2 flex h-10 items-center overflow-hidden rounded-sm px-2 text-sm font-semibold', tone(slot)]}
              style:left="{x(slot.start)}px"
              style:width="{Math.max(6, x(slot.end) - x(slot.start))}px"
              title={tip(slot)}
            >
              <span class="truncate">{slot.job.name.replace(/\+/gu, ' ')}</span>
            </div>
          {/each}
        </div>

        <!-- now -->
        <div class="pointer-events-none absolute inset-y-0 w-0.5 bg-red-600" style:left="{x(now)}px"></div>
      </div>
    </div>
  </div>
{/if}
