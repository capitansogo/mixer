<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import {
    cfg, status,
    type AudioSession, type Config, type MixDevice, type MixSourceState, type MixStatus,
  } from '../lib/stores';
  import {
    AddMixSource,
    GetMixStatus,
    ListAudioSessions,
    ListMixDevices,
    RemoveMixSource,
    SetMixDevices,
    SetMixEnabled,
    SetMixGain,
    SetMixMuted,
    WatchMix,
  } from '../../wailsjs/go/main/App.js';
  import { BrowserOpenURL, EventsOff, EventsOn } from '../../wailsjs/runtime/runtime.js';

  const SEGMENTS = 22;
  const DB_FLOOR = -54; // meter bottom
  const MIC = 'mic';
  const CABLE_URL = 'https://vb-audio.com/Cable/';

  let st: MixStatus | null = null;
  let inputs: MixDevice[] = [];
  let outputs: MixDevice[] = [];
  let sessions: AudioSession[] = [];
  let picking = false;
  let customExe = '';
  let busy = false;

  // Fader drag: while a fader is held, its position is ours, not the
  // backend's (status events would otherwise fight the pointer).
  let dragId: string | null = null;
  let local: Record<string, number> = {};
  // Smoothed meter values (peak with decay), keyed by source id + "out".
  let shown: Record<string, number> = {};

  $: mix = $cfg?.audioMix;
  $: sources = st?.sources ?? [];
  $: live = !!st?.running;
  $: micInputs = inputs.filter((d) => !d.virtual);
  $: added = new Set(sources.map((s) => s.id));
  $: candidates = Array.from(
    new Set(sessions.filter((s) => s.name && !s.isSystem).map((s) => s.name)),
  ).filter((n) => !added.has(n));
  $: noCable = !!st?.error && !st.running && st.error.includes('VB-Cable');

  function setCfg(c: Config | unknown) {
    $cfg = c as Config;
  }

  function onStatus(s: MixStatus) {
    st = s;
    const next: Record<string, number> = {};
    for (const src of s.sources) next[src.id] = decay(src.id, src.level);
    next.out = decay('out', s.outLevel);
    shown = next;
  }

  function decay(id: string, v: number): number {
    const prev = shown[id] ?? 0;
    return v >= prev ? v : Math.max(v, prev * 0.82);
  }

  async function refreshDevices() {
    try {
      const d = await ListMixDevices();
      inputs = d.inputs ?? [];
      outputs = d.outputs ?? [];
    } catch (e) {
      $status = `${e}`;
    }
  }

  async function refreshSessions() {
    try { sessions = await ListAudioSessions(); } catch {}
  }

  async function toggle() {
    if (!mix || busy) return;
    busy = true;
    try {
      const c = (await SetMixEnabled(!mix.enabled)) as unknown as Config;
      setCfg(c);
      $status = c.audioMix.enabled ? 'Аудио-микс включён' : 'Аудио-микс выключен';
    } catch (e) {
      $status = `${e}`;
    }
    busy = false;
  }

  async function setDevices(micId: string, outId: string) {
    try {
      setCfg(await SetMixDevices(micId, outId));
    } catch (e) {
      $status = `${e}`;
    }
  }

  async function toggleMute(src: MixSourceState) {
    try {
      setCfg(await SetMixMuted(src.id, !src.muted));
    } catch (e) {
      $status = `${e}`;
    }
  }

  async function add(exe: string) {
    const v = exe.trim().toLowerCase();
    if (!v) return;
    try {
      setCfg(await AddMixSource(v));
      customExe = '';
      picking = false;
      $status = `${v} добавлен в микс`;
      st = await GetMixStatus();
    } catch (e) {
      $status = `${e}`;
    }
  }

  async function remove(id: string) {
    try {
      setCfg(await RemoveMixSource(id));
      st = await GetMixStatus();
    } catch (e) {
      $status = `${e}`;
    }
  }

  // ---------- fader ----------

  let pendingGain: { id: string; v: number } | null = null;
  let raf = 0;

  function sendGain(id: string, v: number) {
    local = { ...local, [id]: v };
    pendingGain = { id, v };
    if (raf) return;
    raf = requestAnimationFrame(() => {
      raf = 0;
      if (pendingGain) SetMixGain(pendingGain.id, pendingGain.v);
      pendingGain = null;
    });
  }

  function gainOf(src: MixSourceState): number {
    return local[src.id] ?? src.gain;
  }

  function posFromPointer(e: PointerEvent, el: HTMLElement): number {
    const r = el.getBoundingClientRect();
    const pad = 14; // half the cap height: the cap centre tracks the pointer
    const v = (r.bottom - pad - e.clientY) / (r.height - 2 * pad);
    return Math.max(0, Math.min(1, Math.round(v * 200) / 200));
  }

  function down(e: PointerEvent, id: string) {
    const el = e.currentTarget as HTMLElement;
    el.setPointerCapture(e.pointerId);
    dragId = id;
    sendGain(id, posFromPointer(e, el));
  }

  function move(e: PointerEvent, id: string) {
    if (dragId !== id) return;
    sendGain(id, posFromPointer(e, e.currentTarget as HTMLElement));
  }

  // Hand a fader back to the backend stream shortly after the last local
  // change, once the status events carry the new value.
  const releaseTimers: Record<string, ReturnType<typeof setTimeout>> = {};
  function release(id: string) {
    clearTimeout(releaseTimers[id]);
    releaseTimers[id] = setTimeout(() => {
      if (dragId === id) return;
      const { [id]: _, ...rest } = local;
      local = rest;
    }, 300);
  }

  function up(id: string) {
    if (dragId === id) dragId = null;
    release(id);
  }

  function nudge(src: MixSourceState, delta: number) {
    const v = Math.max(0, Math.min(1, Math.round((gainOf(src) + delta) * 100) / 100));
    sendGain(src.id, v);
    release(src.id);
  }

  function wheel(e: WheelEvent, src: MixSourceState) {
    e.preventDefault();
    nudge(src, e.deltaY < 0 ? 0.02 : -0.02);
  }

  function key(e: KeyboardEvent, src: MixSourceState) {
    const step = e.shiftKey ? 0.1 : 0.02;
    if (e.key === 'ArrowUp' || e.key === 'ArrowRight') nudge(src, step);
    else if (e.key === 'ArrowDown' || e.key === 'ArrowLeft') nudge(src, -step);
    else if (e.key === 'm' || e.key === 'M' || e.key === 'ь') toggleMute(src);
    else return;
    e.preventDefault();
  }

  function resetFader(src: MixSourceState) {
    sendGain(src.id, src.id === MIC ? 1 : 0.5);
    release(src.id);
  }

  // ---------- display helpers ----------

  function label(id: string): string {
    return id === MIC ? 'Голос' : id.replace(/\.exe$/, '');
  }

  function gainDb(pos: number): string {
    if (pos <= 0.0005) return '−∞';
    const db = 40 * Math.log10(pos); // amplitude = pos², see mixbus.GainCurve
    if (db > -0.05) return '0.0';
    return `−${Math.abs(db).toFixed(1)}`;
  }

  function meterPct(peak: number): number {
    if (peak <= 0) return 0;
    const db = 20 * Math.log10(peak);
    return Math.max(0, Math.min(1, (db - DB_FLOOR) / -DB_FLOOR));
  }

  function segClass(seg: number, pct: number, muted: boolean): string {
    if (seg + 1 > Math.round(pct * SEGMENTS)) return 'off';
    if (muted) return 'dim';
    if (seg >= SEGMENTS - 2) return 'peak';
    if (seg >= SEGMENTS - 6) return 'high';
    return 'mid';
  }

  function stateText(src: MixSourceState): string {
    if (!live) return 'off';
    if (src.error) return 'error';
    if (src.id === MIC) return src.active ? 'input' : 'no mic';
    return src.active ? 'capture' : 'idle';
  }

  onMount(async () => {
    EventsOn('mix-status', onStatus);
    try { await WatchMix(true); } catch {}
    try { onStatus(await GetMixStatus()); } catch {}
    refreshDevices();
    refreshSessions();
  });

  onDestroy(() => {
    EventsOff('mix-status');
    WatchMix(false);
    if (raf) cancelAnimationFrame(raf);
  });
</script>

<div class="page">
  <header class="head">
    <div>
      <div class="kicker tech-label">virtual mic · bus</div>
      <h1>Аудио-микс</h1>
      <p class="sub">Голос и звук приложений — в один виртуальный микрофон.</p>
    </div>

    <button
      class="power"
      class:on={mix?.enabled}
      class:live
      class:fault={mix?.enabled && !live}
      on:click={toggle}
      disabled={!mix || busy}
    >
      <span class="power-lamp"></span>
      <span class="power-text">
        <span class="tech-label">{mix?.enabled ? (live ? 'on air' : 'нет сигнала') : 'bypass'}</span>
        <span class="power-big">{mix?.enabled ? 'Включено' : 'Выключено'}</span>
      </span>
      <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
        <path d="M11 3h2v10h-2V3zm6.3 2.7 1.4 1.4A8 8 0 1 1 5.3 7.1l1.4-1.4A6 6 0 1 0 17.3 5.7z" fill="currentColor"/>
      </svg>
    </button>
  </header>

  {#if noCable}
    <div class="banner fault">
      <div>
        <strong>Виртуальный кабель не найден.</strong>
        Аудио-микс пишет звук в «CABLE Input» от VB-Cable — это бесплатный драйвер.
        Установи его, перезагрузи ПК и включи микс снова.
      </div>
      <button on:click={() => BrowserOpenURL(CABLE_URL)}>Скачать VB-Cable</button>
    </div>
  {:else if mix?.enabled && st?.error}
    <div class="banner" class:fault={!live}>
      <div>{st.error}</div>
      {#if !live}<span class="tech-label">повтор через 2 с</span>{/if}
    </div>
  {/if}

  <section class="routing">
    <div class="route-cell">
      <div class="tech-label">вход · микрофон</div>
      <select
        value={mix?.micDevice ?? ''}
        on:change={(e) => setDevices(e.currentTarget.value, mix?.outputDevice ?? '')}
      >
        <option value="">Авто — устройство Windows по умолчанию</option>
        {#each micInputs as d}
          <option value={d.id}>{d.name}{d.default ? ' · по умолчанию' : ''}</option>
        {/each}
      </select>
      <div class="route-now tech-data">{live && st?.mic ? st.mic : '—'}</div>
    </div>

    <div class="route-arrow" class:live aria-hidden="true">
      <span></span><span></span><span></span>
      <svg viewBox="0 0 24 24" width="16" height="16"><path d="M13 7l5 5-5 5v-3H6v-4h7V7z" fill="currentColor"/></svg>
    </div>

    <div class="route-cell">
      <div class="tech-label">выход · виртуальный кабель</div>
      <select
        value={mix?.outputDevice ?? ''}
        on:change={(e) => setDevices(mix?.micDevice ?? '', e.currentTarget.value)}
      >
        <option value="">Авто — CABLE Input</option>
        {#each outputs as d}
          <option value={d.id}>{d.name}{d.virtual ? '' : ' · не виртуальный!'}</option>
        {/each}
      </select>
      <div class="route-now tech-data">{live && st?.output ? st.output : '—'}</div>
    </div>

    <button class="ghost refresh" on:click={refreshDevices} title="Обновить устройства">
      <svg viewBox="0 0 24 24" width="14" height="14"><path d="M17.65 6.35A7.96 7.96 0 0 0 12 4a8 8 0 0 0-7.43 11h2.13A6 6 0 0 1 18 12h-3l4 4 4-4h-3a8 8 0 0 0-2.35-5.65Z" fill="currentColor"/></svg>
    </button>
  </section>

  <section class="console" class:bypass={!live}>
    {#each sources as src (src.id)}
      {@const g = gainOf(src)}
      {@const pct = meterPct(shown[src.id] ?? 0)}
      <div class="strip" class:voice={src.id === MIC} class:muted={src.muted} class:idle={live && !src.active}>
        <div class="strip-head">
          <span class="lamp" class:on={live && src.active && !src.muted} class:err={!!src.error}></span>
          <span class="strip-name" title={src.id}>{label(src.id)}</span>
          {#if src.id !== MIC}
            <button class="strip-x" on:click={() => remove(src.id)} title="Убрать из микса">
              <svg viewBox="0 0 24 24" width="11" height="11"><path d="M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z" fill="currentColor"/></svg>
            </button>
          {/if}
        </div>

        <div class="strip-body">
          <div class="meter" aria-hidden="true">
            {#each Array(SEGMENTS) as _, s}
              <span class="seg {segClass(SEGMENTS - 1 - s, pct, src.muted)}"></span>
            {/each}
          </div>

          <!-- svelte-ignore a11y-no-noninteractive-tabindex -->
          <div
            class="fader"
            class:held={dragId === src.id}
            role="slider"
            tabindex="0"
            aria-label={`Громкость: ${label(src.id)}`}
            aria-valuemin={0}
            aria-valuemax={100}
            aria-valuenow={Math.round(g * 100)}
            on:pointerdown={(e) => down(e, src.id)}
            on:pointermove={(e) => move(e, src.id)}
            on:pointerup={() => up(src.id)}
            on:pointercancel={() => up(src.id)}
            on:wheel={(e) => wheel(e, src)}
            on:keydown={(e) => key(e, src)}
            on:dblclick={() => resetFader(src)}
          >
            <div class="ticks">
              {#each [0, 1, 2, 3, 4, 5, 6, 7, 8] as t}<span class:major={t % 2 === 0}></span>{/each}
            </div>
            <div class="slot"></div>
            <div class="slot-fill" style="height: calc((100% - 28px) * {g})"></div>
            <div class="cap" style="bottom: calc((100% - 28px) * {g})">
              <span class="cap-line"></span>
            </div>
          </div>
        </div>

        <div class="strip-read tech-data">
          <span class="pct">{Math.round(g * 100)}<span class="unit">%</span></span>
          <span class="db">{gainDb(g)} dB</span>
        </div>

        <button class="mute" class:on={src.muted} on:click={() => toggleMute(src)} title="Заглушить в миксе (M)">
          M
        </button>

        <div class="strip-state tech-label" class:err={!!src.error} title={src.error || ''}>
          {stateText(src)}
        </div>
      </div>
    {/each}

    <button class="strip add-strip" class:open={picking} on:click={() => { picking = !picking; if (picking) refreshSessions(); }}>
      <svg viewBox="0 0 24 24" width="22" height="22"><path d="M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z" fill="currentColor"/></svg>
      <span>Добавить<br/>приложение</span>
    </button>

    <div class="strip master">
      <div class="strip-head">
        <span class="lamp" class:on={live}></span>
        <span class="strip-name">Выход</span>
      </div>
      <div class="strip-body">
        <div class="meter wide" aria-hidden="true">
          {#each Array(SEGMENTS) as _, s}
            <span class="seg {segClass(SEGMENTS - 1 - s, meterPct(shown.out ?? 0), false)}"></span>
          {/each}
        </div>
      </div>
      <div class="strip-read tech-data">
        <span class="pct">{live ? 'LIVE' : '— —'}</span>
        <span class="db">в CABLE</span>
      </div>
    </div>
  </section>

  {#if picking}
    <section class="picker">
      <div class="pick-section">
        <div class="tech-label">Сейчас играют</div>
        <div class="pick-row">
          {#if candidates.length === 0}
            <span class="muted">нет новых аудиосессий — запусти звук в приложении или впиши exe ниже</span>
          {/if}
          {#each candidates as name}
            <button class="pick-btn" on:click={() => add(name)}>{name}</button>
          {/each}
          <button class="pick-btn ghost" on:click={refreshSessions} title="Обновить">
            <svg viewBox="0 0 24 24" width="12" height="12"><path d="M17.65 6.35A7.96 7.96 0 0 0 12 4a8 8 0 0 0-7.43 11h2.13A6 6 0 0 1 18 12h-3l4 4 4-4h-3a8 8 0 0 0-2.35-5.65Z" fill="currentColor"/></svg>
          </button>
        </div>
      </div>
      <div class="pick-section">
        <div class="tech-label">Вручную</div>
        <div class="pick-row">
          <input
            type="text"
            placeholder="telegram.exe"
            bind:value={customExe}
            on:keydown={(e) => e.key === 'Enter' && add(customExe)}
          />
          <button class="pick-btn" on:click={() => add(customExe)}>Добавить</button>
        </div>
      </div>
    </section>
  {/if}

  <section class="howto">
    <div class="tech-label">как подключить</div>
    <ol>
      <li>В Discord / OBS / Telegram выбери микрофоном <b>CABLE Output</b>.</li>
      <li>В Discord выключи <b>шумоподавление</b> (Krisp) и <b>эхоподавление</b> — иначе музыку будет резать.</li>
      <li>Ползунок железа можно привязать к громкости в миксе: «Привязки» → «Аудио-микс».</li>
    </ol>
  </section>
</div>

<style>
  .page { padding: 28px 32px 32px; }

  .head {
    display: flex;
    justify-content: space-between;
    align-items: flex-end;
    gap: 24px;
    margin-bottom: 20px;
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
  .sub { color: var(--text-dim); margin: 8px 0 0; font-size: 0.9rem; }

  button {
    background: var(--bg-elevated);
    color: var(--text);
    border: 1px solid var(--line);
    border-radius: 5px;
    padding: 8px 14px;
    cursor: pointer;
    font-family: inherit;
    font-size: 0.85rem;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    transition: all 160ms var(--ease-out);
  }
  button:hover:not(:disabled) { border-color: var(--line-strong); background: var(--bg-hover); }
  button:disabled { opacity: 0.45; cursor: not-allowed; }
  button.ghost { background: transparent; }

  /* ============ power switch ============ */
  .power {
    gap: 14px;
    padding: 10px 16px 10px 14px;
    border-radius: 8px;
    background: var(--bg-panel);
    color: var(--text-soft);
  }
  .power svg { color: var(--text-dim); }
  .power-lamp {
    width: 10px;
    height: 10px;
    border-radius: 50%;
    background: var(--text-faint);
    box-shadow: inset 0 0 2px rgba(0, 0, 0, 0.6);
  }
  .power-text { display: flex; flex-direction: column; align-items: flex-start; gap: 2px; }
  .power-big {
    font-family: var(--font-display);
    font-weight: 700;
    font-size: 1.05rem;
    color: var(--text-bright);
    letter-spacing: -0.01em;
  }
  .power.on { border-color: rgba(255, 122, 24, 0.35); }
  .power.on svg { color: var(--amber); }
  .power.on .power-lamp { background: var(--amber); box-shadow: 0 0 8px var(--amber-glow); }
  .power.live { border-color: rgba(74, 222, 128, 0.35); }
  .power.live svg { color: var(--signal); }
  .power.live .tech-label { color: var(--signal); }
  .power.live .power-lamp {
    background: var(--signal);
    box-shadow: 0 0 10px var(--signal-glow);
    animation: pulse-signal 1.8s ease-in-out infinite;
  }
  .power.fault .tech-label { color: var(--danger); }

  /* ============ banners ============ */
  .banner {
    display: flex;
    align-items: center;
    justify-content: space-between;
    gap: 16px;
    padding: 12px 16px;
    margin-bottom: 14px;
    border-radius: 8px;
    border: 1px solid rgba(255, 165, 100, 0.3);
    background: rgba(255, 122, 24, 0.07);
    color: var(--amber-soft);
    font-size: 0.86rem;
    line-height: 1.45;
  }
  .banner.fault {
    border-color: rgba(255, 77, 106, 0.35);
    background: rgba(255, 77, 106, 0.07);
    color: #ffb3c0;
  }
  .banner strong { color: var(--text-bright); }
  .banner button { flex-shrink: 0; }

  /* ============ routing ============ */
  .routing {
    display: grid;
    grid-template-columns: 1fr auto 1fr auto;
    gap: 16px;
    align-items: start;
    background: var(--bg-panel);
    border: 1px solid var(--line);
    border-radius: 10px;
    padding: 16px 18px;
    margin-bottom: 14px;
  }
  .route-cell { display: flex; flex-direction: column; gap: 7px; min-width: 0; }
  .route-now {
    font-size: 0.72rem;
    color: var(--text-dim);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  select {
    background: var(--bg-elevated);
    color: var(--text);
    border: 1px solid var(--line);
    border-radius: 5px;
    padding: 7px 10px;
    font-size: 0.84rem;
    width: 100%;
    min-width: 0;
  }
  select:focus { outline: none; border-color: var(--amber); }
  .route-arrow {
    display: flex;
    align-items: center;
    gap: 4px;
    padding-top: 26px;
    color: var(--text-faint);
  }
  .route-arrow span {
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: currentColor;
  }
  .route-arrow.live { color: var(--signal); }
  .route-arrow.live span { animation: flow 1.2s linear infinite; }
  .route-arrow.live span:nth-child(2) { animation-delay: 0.2s; }
  .route-arrow.live span:nth-child(3) { animation-delay: 0.4s; }
  @keyframes flow {
    0%, 100% { opacity: 0.25; }
    40% { opacity: 1; }
  }
  .refresh { margin-top: 22px; padding: 8px 9px; }

  /* ============ console ============ */
  .console {
    display: flex;
    gap: 10px;
    overflow-x: auto;
    background: var(--bg-panel);
    border: 1px solid var(--line);
    border-radius: 10px;
    padding: 18px 16px 16px;
    position: relative;
  }
  .console::before {
    content: "";
    position: absolute;
    inset: 0;
    background:
      repeating-linear-gradient(0deg, transparent 0, transparent 39px, rgba(255,255,255,0.012) 40px),
      repeating-linear-gradient(90deg, transparent 0, transparent 39px, rgba(255,255,255,0.012) 40px);
    pointer-events: none;
  }

  .strip {
    position: relative;
    z-index: 1;
    flex: 0 0 104px;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 10px;
    padding: 10px 8px 12px;
    background: linear-gradient(180deg, var(--bg-elevated) 0%, rgba(27, 27, 37, 0.4) 100%);
    border: 1px solid var(--line);
    border-radius: 8px;
    --accent: var(--amber);
    --accent-soft: var(--amber-soft);
    --accent-glow: var(--amber-glow);
  }
  .strip.voice {
    --accent: var(--info);
    --accent-soft: #a9d3ff;
    --accent-glow: rgba(111, 182, 255, 0.35);
    border-color: rgba(111, 182, 255, 0.22);
  }
  .strip.master {
    --accent: var(--signal);
    --accent-glow: var(--signal-glow);
    margin-left: auto;
    flex-basis: 84px;
    border-color: rgba(74, 222, 128, 0.18);
  }
  .strip.idle .strip-name { color: var(--text-soft); }

  .strip-head {
    width: 100%;
    display: flex;
    align-items: center;
    gap: 7px;
    min-height: 18px;
  }
  .lamp {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    flex-shrink: 0;
    background: var(--text-faint);
  }
  .lamp.on { background: var(--accent); box-shadow: 0 0 7px var(--accent-glow); }
  .lamp.err { background: var(--danger); box-shadow: 0 0 7px rgba(255, 77, 106, 0.5); }
  .strip-name {
    flex: 1;
    min-width: 0;
    font-family: var(--font-mono);
    font-size: 0.78rem;
    font-weight: 600;
    color: var(--text-bright);
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
  }
  .strip-x {
    background: transparent;
    border: none;
    padding: 0 2px;
    color: var(--text-faint);
  }
  .strip-x:hover:not(:disabled) { color: var(--danger); background: transparent; }

  .strip-body {
    display: flex;
    gap: 8px;
    height: 250px;
    align-items: stretch;
  }

  /* meter */
  .meter {
    width: 14px;
    display: flex;
    flex-direction: column;
    gap: 2px;
    padding: 3px;
    background: linear-gradient(180deg, #08080b 0%, #0c0c11 100%);
    border: 1px solid var(--line);
    border-radius: 4px;
    box-shadow: inset 0 2px 6px rgba(0, 0, 0, 0.6);
  }
  .meter.wide { width: 34px; }
  .seg {
    flex: 1;
    border-radius: 1px;
    background: rgba(255, 255, 255, 0.03);
    transition: background 70ms linear;
  }
  .seg.mid  { background: #4a9b6b; }
  .seg.high { background: #ffaa33; }
  .seg.peak { background: #ff4040; }
  .seg.dim  { background: rgba(255, 255, 255, 0.14); }

  /* fader */
  .fader {
    position: relative;
    width: 46px;
    cursor: ns-resize;
    touch-action: none;
    border-radius: 4px;
    outline: none;
  }
  .fader:focus-visible { box-shadow: 0 0 0 1px var(--accent); }
  .ticks {
    position: absolute;
    inset: 14px 0;
    display: flex;
    flex-direction: column;
    justify-content: space-between;
    pointer-events: none;
  }
  .ticks span {
    display: block;
    height: 1px;
    width: 7px;
    background: var(--text-faint);
    opacity: 0.6;
  }
  .ticks span.major { width: 11px; opacity: 1; }
  .slot {
    position: absolute;
    left: 50%;
    top: 14px;
    bottom: 14px;
    width: 4px;
    transform: translateX(-50%);
    background: #050507;
    border-radius: 2px;
    box-shadow: inset 0 1px 2px rgba(0, 0, 0, 0.9), 0 0 0 1px rgba(255, 255, 255, 0.04);
  }
  .slot-fill {
    position: absolute;
    left: 50%;
    bottom: 14px;
    width: 4px;
    transform: translateX(-50%);
    background: var(--accent);
    opacity: 0.55;
    border-radius: 2px;
    box-shadow: 0 0 6px var(--accent-glow);
  }
  .strip.muted .slot-fill { background: var(--text-faint); box-shadow: none; }
  .cap {
    position: absolute;
    left: 50%;
    width: 38px;
    height: 28px;
    transform: translateX(-50%);
    border-radius: 4px;
    background: linear-gradient(180deg, #3a3a48 0%, #23232d 48%, #1a1a22 52%, #2b2b36 100%);
    border: 1px solid rgba(255, 255, 255, 0.12);
    box-shadow: 0 4px 10px rgba(0, 0, 0, 0.55), inset 0 1px 0 rgba(255, 255, 255, 0.14);
    display: grid;
    place-items: center;
    transition: transform 120ms var(--ease-out);
  }
  .cap-line {
    width: 26px;
    height: 2px;
    border-radius: 1px;
    background: var(--accent);
    box-shadow: 0 0 6px var(--accent-glow);
  }
  .strip.muted .cap-line { background: var(--text-faint); box-shadow: none; }
  .fader:hover .cap { border-color: rgba(255, 255, 255, 0.22); }
  .fader.held .cap { transform: translateX(-50%) scale(1.05); border-color: var(--accent); }

  .strip-read {
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 1px;
    line-height: 1.1;
  }
  .pct { font-size: 1.15rem; font-weight: 600; color: var(--text-bright); }
  .pct .unit { font-size: 0.7rem; color: var(--text-dim); margin-left: 1px; }
  .db { font-size: 0.68rem; color: var(--text-dim); }

  .mute {
    width: 100%;
    justify-content: center;
    padding: 5px 0;
    font-family: var(--font-mono);
    font-weight: 700;
    font-size: 0.78rem;
    letter-spacing: 0.08em;
    color: var(--text-dim);
  }
  .mute.on {
    background: rgba(255, 77, 106, 0.16);
    border-color: rgba(255, 77, 106, 0.55);
    color: var(--danger);
    box-shadow: 0 0 10px -2px rgba(255, 77, 106, 0.5);
  }
  .strip.muted .strip-read .pct { color: var(--text-dim); }

  .strip-state { font-size: 0.6rem; letter-spacing: 0.16em; }
  .strip-state.err { color: var(--danger); cursor: help; }

  .add-strip {
    flex: 0 0 96px;
    flex-direction: column;
    justify-content: center;
    gap: 10px;
    background: transparent;
    border: 1px dashed var(--line-strong);
    color: var(--text-dim);
    text-align: center;
    font-size: 0.8rem;
    line-height: 1.3;
  }
  .add-strip:hover:not(:disabled), .add-strip.open {
    color: var(--amber);
    border-color: var(--amber);
    background: rgba(255, 122, 24, 0.04);
  }

  .console.bypass .seg.mid,
  .console.bypass .seg.high,
  .console.bypass .seg.peak { background: rgba(255, 255, 255, 0.03); }

  /* ============ picker ============ */
  .picker {
    margin-top: 12px;
    padding: 16px;
    background: var(--bg-base);
    border: 1px solid var(--line);
    border-radius: 8px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .pick-section { display: flex; flex-direction: column; gap: 8px; }
  .pick-row { display: flex; flex-wrap: wrap; gap: 6px; align-items: center; }
  .pick-btn {
    font-family: var(--font-mono);
    font-size: 0.8rem;
    padding: 6px 12px;
    border-radius: 4px;
  }
  .pick-btn:hover:not(:disabled) { border-color: var(--amber); }
  .pick-btn.ghost { padding: 6px 8px; }
  input[type="text"] {
    background: var(--bg-elevated);
    border: 1px solid var(--line);
    color: var(--text);
    border-radius: 4px;
    padding: 6px 12px;
    font-size: 0.85rem;
    font-family: var(--font-mono);
    flex: 1;
    min-width: 180px;
  }
  input:focus { outline: none; border-color: var(--amber); }
  .muted { color: var(--text-dim); font-size: 0.8rem; }

  /* ============ how-to ============ */
  .howto {
    margin-top: 14px;
    padding: 14px 18px;
    border: 1px solid var(--line-soft);
    border-radius: 8px;
    color: var(--text-soft);
    font-size: 0.84rem;
  }
  .howto ol { margin: 8px 0 0; padding-left: 18px; display: flex; flex-direction: column; gap: 4px; }
  .howto b { color: var(--text); font-weight: 600; }
</style>
