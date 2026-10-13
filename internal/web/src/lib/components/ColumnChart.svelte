<script lang="ts">
  import { ChartCore, Svg, Bar, Axis, Tooltip } from 'layerchart/svg';
  // Columns over time, one or several stacked series, in inline SVG that
  // fills its container's width. Pointer and arrow keys read one column at
  // a time; the table view carries every value without hovering.
  interface Series {
    label: string;
    // color is a CSS colour, normally a --chart-* token.
    color: string;
  }
  interface Row {
    key: string;
    // label names the column on the axis, and title in the readout.
    label: string;
    title: string;
    // values are the series' in order, bottom of the stack first.
    values: number[];
  }
  interface Props {
    label: string;
    series: Series[];
    rows: Row[];
    format: (n: number) => string;
    // whole says the values are counts, so every gridline is a whole number.
    whole?: boolean;
    view?: 'chart' | 'table';
    height?: number;
  }
  let { label, series, rows, format, whole = false, view = 'chart', height = 180 }: Props = $props();

  let width = $state(0);
  const totals = $derived(rows.map((r) => r.values.reduce((a, b) => a + b, 0)));

  // niceMax rounds the tallest column up to 1, 2, 2.5 or 5 times a power of
  // ten, so the gridlines fall on clean numbers. Counts also take 4 and 6,
  // and only a top whose half is whole: the middle gridline is that half.
  function niceMax(v: number): number {
    if (v <= 0) return whole ? 2 : 1;
    const p = 10 ** Math.floor(Math.log10(v));
    const steps = whole ? [1, 2, 2.5, 4, 5, 6, 10].filter((m) => Number.isInteger((m * p) / 2)) : [1, 2, 2.5, 5, 10];
    return (steps.find((m) => m * p >= v) ?? 10) * p;
  }
  const max = $derived(niceMax(Math.max(0, ...totals)));
  const ticks = $derived([0, max / 2, max]);
  // The left margin holds the widest axis label, at about 6.5px a character
  // of the 10px tick text, so a long one ($2,000.00) is not cut off.
  const pad = $derived({ top: 10, right: 6, bottom: 22, left: Math.max(46, 12 + 6.5 * Math.max(...ticks.map((t) => format(t).length))) });
  const plotW = $derived(Math.max(0, width - pad.left - pad.right));
  const band = $derived(rows.length ? plotW / rows.length : 0);
  const barW = $derived(Math.max(2, Math.min(24, band * 0.64)));
  const every = $derived(Math.max(1, Math.ceil(rows.length / Math.max(1, Math.floor(plotW / 48)))));
  const chartSeries = $derived(series.map((s, i) => ({ key: s.label, label: s.label, color: s.color, value: (r: Row) => r.values[i]! })));

  let active = $state(-1);
  function onpointermove(e: PointerEvent): void {
    const box = (e.currentTarget as SVGElement).getBoundingClientRect();
    const i = Math.floor((e.clientX - box.left - pad.left) / band);
    active = i >= 0 && i < rows.length ? i : -1;
  }
  function onkeydown(e: KeyboardEvent): void {
    if (!rows.length) return;
    if (e.key === 'ArrowRight') active = Math.min(rows.length - 1, active + 1);
    else if (e.key === 'ArrowLeft') active = active <= 0 ? 0 : active - 1;
    else if (e.key === 'Home') active = 0;
    else if (e.key === 'End') active = rows.length - 1;
    else if (e.key === 'Escape') active = -1;
    else return;
    e.preventDefault();
  }
  const activeRow = $derived(active >= 0 ? rows[active] : undefined);
</script>

{#if view === 'table'}
  <div class="table-wrap chart-table max-w-full max-h-70 overflow-auto">
    <table class="data">
      <thead>
        <tr>
          <th scope="col">Date</th>
          {#each series as s (s.label)}<th scope="col" class="num">{s.label}</th>{/each}
          {#if series.length > 1}<th scope="col" class="num">Total</th>{/if}
        </tr>
      </thead>
      <tbody>
        <!-- Newest first, where the chart reads oldest first. -->
        {#each [...rows].reverse() as r, i (r.key)}
          <tr>
            <td>{r.title}</td>
            {#each r.values as v, j (j)}<td class="num">{format(v)}</td>{/each}
            {#if series.length > 1}<td class="num">{format(totals[rows.length - 1 - i]!)}</td>{/if}
          </tr>
        {/each}
      </tbody>
    </table>
  </div>
{:else}
  {#if series.length > 1}
    <ul class="chart-legend flex flex-wrap gap-y-1 gap-x-3.5 mt-2.5 mx-3 mb-0 p-0 list-none text-ui-sm text-muted" aria-label="{label} legend">
      {#each series as s (s.label)}<li><span class="chart-swatch w-2.5 h-2.5 rounded-[2px]" style:background={s.color}></span>{s.label}</li>{/each}
    </ul>
  {/if}
  <div class="column-chart relative pt-2 px-3 pb-2.5">
    <div bind:clientWidth={width}>
      {#if width > 0}
        <ChartCore
          data={rows}
          x="key"
          valueAxis="y"
          series={chartSeries}
          seriesLayout="stack"
          bandPadding={0}
          yDomain={[0, max]}
          yNice={false}
          padding={pad}
          {width}
          {height}
        >
          {#snippet children({ context })}
            <Svg
              class="chart block font-ui"
              role="img"
              aria-label="{label}. Arrow keys read one column at a time."
              tabindex={0}
              {onpointermove}
              onpointerleave={() => (active = -1)}
              {onkeydown}
              onblur={() => (active = -1)}
            >
              <Axis aria-hidden="true" placement="left" {ticks} {format} tickMarks={false} grid={{ class: 'chart-grid' }} classes={{ tickLabel: 'chart-tick' }} />
              <Axis aria-hidden="true" placement="bottom" ticks={rows.filter((_, i) => i % every === 0).map((r) => r.key)} format={(key) => rows.find((r) => r.key === key)?.label ?? ''} tickMarks={false} classes={{ tickLabel: 'chart-tick' }} />
              {#if active >= 0}
                <rect class="chart-hover fill-[color-mix(in_srgb,_var(--text)_5%,_transparent)]" x={band * active} y={0} width={band} height={context.height} />
              {/if}
              {#each rows as r, i (r.key)}
                <g class="chart-col [transition:opacity_0.1s]" class:dim={active >= 0 && active !== i}>
                  {#each series as s, j (s.label)}
                    {#if r.values[j]! > 0}
                      <Bar
                        data={r}
                        seriesKey={s.label}
                        fill={s.color}
                        width={barW}
                        stackPadding={2}
                        radius={4}
                        rounded={context.series.isStackTop(s.label, r) ? 'top' : 'none'}
                      />
                    {/if}
                  {/each}
                </g>
              {/each}
            </Svg>
            {#if activeRow}
              <Tooltip.Root data={activeRow} x="data" y={pad.top} xOffset={12} portal={false} variant="none" motion="none" fadeDuration={0} class="chart-tip min-w-30 py-2 px-2.5 pointer-events-none bg-panel border border-solid border-line rounded-lg shadow-[0_6px_18px_color-mix(in_srgb,_var(--text)_12%,_transparent)] text-ui-sm" classes={{ root: 'chart-tooltip' }}>
                <p class="chart-tip-head mt-0 mx-0 mb-1 text-muted text-ui-xs">{activeRow.title}</p>
                {#each [...series.keys()].reverse() as j (j)}
                  <p class="chart-tip-row flex items-center gap-1.5 m-0 whitespace-nowrap">
                    <span class="chart-key w-2.5 h-0.5 rounded-[1px]" style:background={series[j]!.color}></span>
                    <strong>{format(activeRow.values[j]!)}</strong>
                    <span class="muted">{series[j]!.label}</span>
                  </p>
                {/each}
              </Tooltip.Root>
            {/if}
          {/snippet}
        </ChartCore>
      {/if}
    </div>
    <p class="sr-only" aria-live="polite">
      {#if activeRow}{activeRow.title}: {series.map((s, j) => `${format(activeRow.values[j]!)} ${s.label}`).join(', ')}{/if}
    </p>
  </div>
{/if}
