<script lang="ts">
  import { values, cfg, connected } from '../lib/stores';

  const SLIDER_MAX = 1023;
  const SEGMENTS = 24;

  $: mapping = $cfg?.sliderMapping ?? {};
  $: calibration = $cfg?.calibration ?? {};

  function pct(i: number, raw: number): number {
    const cal = calibration[i] ?? { min: 0, max: SLIDER_MAX };
    const span = Math.max(cal.max - cal.min, 50);
    const p = ((raw - cal.min) / span) * 100;
    return Math.max(0, Math.min(100, p));
  }

  // 0..1 for segment lighting decision
  function lit(segIdx: number, percent: number): boolean {
    return (segIdx + 1) <= Math.round((percent / 100) * SEGMENTS);
  }

  function segColor(segIdx: number, percent: number): string {
    if (!lit(segIdx, percent)) return 'off';
    if (segIdx >= SEGMENTS - 3) return 'peak';
    if (segIdx >= SEGMENTS - 8) return 'high';
    return 'mid';
  }
</script>

<div class="page">
  <header class="head">
    <div>
      <div class="kicker tech-label">channel 01 — 05 · live</div>
      <h1>Главная</h1>
    </div>
    <div class="meta tech-data">
      <span class="meta-cell">
        <span class="tech-label">channels</span>
        <span class="big">{$values.length}</span>
      </span>
      <span class="divider"></span>
      <span class="meta-cell">
        <span class="tech-label">link</span>
        <span class="big" class:on={$connected}>
          {$connected ? 'LIVE' : '— —'}
        </span>
      </span>
    </div>
  </header>

  <section class="rack">
    {#each $values as v, i}
      {@const p = pct(i, v)}
      <div class="channel">
        <div class="ch-head">
          <span class="ch-num tech-data">0{i + 1}</span>
          <span class="ch-pct tech-data">{Math.round(p)}<span class="unit">%</span></span>
        </div>

        <div class="meter">
          <div class="segments">
            {#each Array(SEGMENTS) as _, s}
              <span class="seg {segColor(SEGMENTS - 1 - s, p)}"></span>
            {/each}
          </div>
          <div class="meter-fill" style="height: {p}%"></div>
        </div>

        <div class="ch-raw tech-data">
          <span class="raw-label">raw</span>
          <span class="raw-val">{v}</span>
        </div>

        <div class="ch-targets">
          {#each (mapping[i] ?? []) as t}
            <span class="chip">{t}</span>
          {/each}
          {#if !(mapping[i]?.length)}
            <span class="chip-empty">— нет привязки —</span>
          {/if}
        </div>
      </div>
    {/each}
  </section>
</div>

<style>
  .page {
    padding: 28px 32px 32px;
  }

  .head {
    display: flex;
    justify-content: space-between;
    align-items: flex-end;
    margin-bottom: 28px;
    gap: 24px;
  }
  .kicker { display: block; margin-bottom: 6px; color: var(--amber); }
  h1 {
    font-family: var(--font-display);
    font-weight: 700;
    font-size: 2.4rem;
    margin: 0;
    line-height: 1;
    letter-spacing: -0.02em;
    color: var(--text-bright);
  }

  .meta {
    display: flex;
    align-items: center;
    gap: 22px;
    background: var(--bg-panel);
    border: 1px solid var(--line);
    border-radius: 8px;
    padding: 12px 22px;
  }
  .meta-cell {
    display: flex;
    flex-direction: column;
    gap: 2px;
    align-items: flex-start;
  }
  .meta-cell .big {
    font-family: var(--font-mono);
    font-weight: 600;
    font-size: 1.1rem;
    color: var(--text-soft);
    letter-spacing: 0.05em;
  }
  .meta-cell .big.on {
    color: var(--signal);
    text-shadow: 0 0 12px var(--signal-glow);
  }
  .divider {
    width: 1px;
    height: 28px;
    background: var(--line);
  }

  /* ============ rack of channels ============ */
  .rack {
    display: grid;
    grid-template-columns: repeat(5, 1fr);
    gap: 16px;
    background: var(--bg-panel);
    border: 1px solid var(--line);
    border-radius: 10px;
    padding: 24px 20px 22px;
    position: relative;
    overflow: hidden;
  }
  .rack::before {
    content: "";
    position: absolute;
    inset: 0;
    background:
      repeating-linear-gradient(0deg,
        transparent 0,
        transparent 39px,
        rgba(255, 255, 255, 0.012) 40px),
      repeating-linear-gradient(90deg,
        transparent 0,
        transparent 39px,
        rgba(255, 255, 255, 0.012) 40px);
    pointer-events: none;
  }

  .channel {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    padding: 8px 0;
    position: relative;
    z-index: 1;
  }

  .ch-head {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 2px;
    text-align: center;
  }
  .ch-num {
    font-size: 0.7rem;
    color: var(--text-dim);
    letter-spacing: 0.18em;
    font-weight: 600;
  }
  .ch-pct {
    font-size: 1.6rem;
    font-weight: 600;
    color: var(--text-bright);
    line-height: 1;
    letter-spacing: -0.01em;
  }
  .ch-pct .unit { font-size: 0.8rem; color: var(--text-dim); margin-left: 2px; }

  /* ============ VU meter ============ */
  .meter {
    width: 44px;
    height: 280px;
    background: linear-gradient(180deg, #08080b 0%, #0c0c11 100%);
    border: 1px solid var(--line);
    border-radius: 5px;
    padding: 4px;
    box-shadow: inset 0 2px 6px rgba(0,0,0,0.6), inset 0 0 0 1px rgba(255,255,255,0.02);
    position: relative;
    display: flex;
    align-items: stretch;
  }
  .segments {
    flex: 1;
    display: flex;
    flex-direction: column;
    gap: 2px;
  }
  .seg {
    flex: 1;
    border-radius: 1.5px;
    background: rgba(255, 255, 255, 0.025);
    /* background only: animating box-shadow on 120 segments at 30 fps
       repaints the whole meter column every frame */
    transition: background 80ms linear;
  }
  .seg.mid {
    background: #4a9b6b;
    box-shadow: 0 0 4px rgba(74, 222, 128, 0.35);
  }
  .seg.high {
    background: #ffaa33;
    box-shadow: 0 0 6px rgba(255, 122, 24, 0.5);
  }
  .seg.peak {
    background: #ff4040;
    box-shadow: 0 0 8px rgba(255, 64, 64, 0.7);
  }
  .meter-fill {
    /* Invisible — actual visualization is the segments above.
       Kept as a 0-height marker for potential future animations. */
    display: none;
  }

  .ch-raw {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1px;
    margin-top: 2px;
  }
  .raw-label {
    font-size: 0.6rem;
    color: var(--text-faint);
    letter-spacing: 0.18em;
    text-transform: uppercase;
  }
  .raw-val {
    font-size: 0.78rem;
    color: var(--text-dim);
  }

  .ch-targets {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 4px;
    margin-top: 4px;
    min-height: 28px;
    max-width: 130px;
  }
  .chip {
    font-family: var(--font-mono);
    font-size: 0.68rem;
    color: var(--amber-soft);
    background: rgba(255, 122, 24, 0.08);
    border: 1px solid rgba(255, 122, 24, 0.24);
    border-radius: 3px;
    padding: 1px 7px;
    white-space: nowrap;
    letter-spacing: 0.01em;
  }
  .chip-empty {
    font-family: var(--font-mono);
    font-size: 0.65rem;
    color: var(--text-faint);
    letter-spacing: 0.05em;
  }
</style>
