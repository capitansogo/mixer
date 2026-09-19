<script lang="ts">
  import { onMount } from 'svelte';
  import { cfg, connected, status, deviceState } from '../lib/stores';
  import { RequestDeviceState, SendCommand, SetLEDMode } from '../../wailsjs/go/main/App.js';

  let activeTheme = 0;
  let brightness = 20;

  $: ledMode = $cfg?.ledMode ?? 0;

  // The device is the source of truth for theme/brightness (it keeps them
  // in EEPROM and can change them via slider gestures). Whenever it
  // reports STATE, mirror it into the controls.
  $: if ($deviceState) {
    activeTheme = $deviceState.theme;
    brightness = $deviceState.brightness;
  }

  onMount(() => {
    if ($connected) RequestDeviceState().catch(() => {});
  });

  const MODES = [
    {
      id: 0, key: 'POS', label: 'Позиция',
      desc: 'Уровень показывает физическое положение ползунка',
    },
    {
      id: 1, key: 'RBW', label: 'Радуга',
      desc: 'Плавная HSV-анимация, темы игнорируются',
    },
    {
      id: 2, key: 'EQ', label: 'Эквалайзер',
      desc: 'Ленты дышат под peak meter привязанных приложений',
    },
  ];

  const THEMES = [
    { id: 0, name: 'Universal',   swatch: ['#0095ff', '#9900ff', '#ff0000'] },
    { id: 1, name: 'Streamer',    swatch: ['#5865f2', '#1ed760', '#ffe600'] },
    { id: 2, name: 'Red',         swatch: ['#ff0000', '#ff0000', '#ff0000'] },
    { id: 3, name: 'White',       swatch: ['#ffffff', '#ffffff', '#ffffff'] },
    { id: 4, name: 'Blue',        swatch: ['#0066ff', '#0066ff', '#0066ff'] },
    { id: 5, name: 'VU Classic',  swatch: ['#00ff0d', '#ffe600', '#ff0000'] },
    { id: 6, name: 'Pink',        swatch: ['#00ffff', '#ff54f9', '#ffa6fc'] },
  ];

  async function send(cmd: string) {
    try {
      await SendCommand(cmd);
      $status = `→ ${cmd}`;
    } catch (e) {
      $status = `${e}`;
    }
  }

  function pickTheme(id: number) {
    activeTheme = id;
    send(`T:${id}`);
  }

  $: syncHint = $connected && !$deviceState
    ? 'Ожидаю STATE от устройства (старая прошивка не отвечает на GET)'
    : '';

  function applyBrightness() {
    send(`B:${brightness}`);
  }

  async function pickMode(id: number) {
    try {
      await SetLEDMode(id);
      if ($cfg) $cfg = { ...$cfg, ledMode: id };
      $status = `Режим лент · ${MODES[id].label}`;
    } catch (e) {
      $status = `${e}`;
    }
  }
</script>

<div class="page">
  <header class="head">
    <div>
      <div class="kicker tech-label">led control</div>
      <h1>Темы LED</h1>
      <p class="sub">Режим работы, цветовая схема и яркость лент. Команды идут на ESP32 сразу.</p>
    </div>
  </header>

  {#if !$connected}
    <div class="warning">
      <svg viewBox="0 0 24 24" width="14" height="14"><path d="M12 5.99 19.53 19H4.47L12 5.99M12 2 1 21h22L12 2zm1 14h-2v2h2v-2zm0-6h-2v4h2v-4z" fill="currentColor"/></svg>
      Не подключено — изменения никуда не уйдут. Подключись на главной.
    </div>
  {:else if syncHint}
    <div class="warning soft">{syncHint}</div>
  {/if}

  <section class="card">
    <div class="card-head">
      <h2>Режим лент</h2>
      <div class="tech-label">mode</div>
    </div>
    <div class="modes">
      {#each MODES as m}
        <button
          class="mode"
          class:active={ledMode === m.id}
          on:click={() => pickMode(m.id)}
          disabled={!$connected}
        >
          <span class="mode-key tech-data">{m.key}</span>
          <span class="mode-label">{m.label}</span>
          <span class="mode-desc">{m.desc}</span>
          {#if ledMode === m.id}
            <span class="mode-active-bar"></span>
          {/if}
        </button>
      {/each}
    </div>
  </section>

  <section class="card">
    <div class="card-head">
      <h2>Цветовая схема</h2>
      <div class="tech-label">palette</div>
    </div>
    <div class="themes">
      {#each THEMES as t}
        <button
          class="theme"
          class:active={activeTheme === t.id}
          on:click={() => pickTheme(t.id)}
          disabled={!$connected}
        >
          <div class="swatch">
            {#each t.swatch as c}<div style="background: {c}"></div>{/each}
          </div>
          <div class="theme-meta">
            <span class="theme-name">{t.name}</span>
            <span class="theme-id tech-data">#0{t.id}</span>
          </div>
        </button>
      {/each}
    </div>
  </section>

  <section class="card">
    <div class="card-head">
      <h2>Яркость</h2>
      <div class="tech-label">brightness</div>
    </div>
    <div class="brightness">
      <input
        type="range"
        min="0"
        max="50"
        bind:value={brightness}
        on:change={applyBrightness}
        disabled={!$connected}
      />
      <div class="bri-value">
        <span class="bri-num tech-data">{brightness}</span>
        <span class="bri-max">/50</span>
      </div>
    </div>
  </section>

  <section class="card service">
    <div class="card-head">
      <h2>Сервис</h2>
      <div class="tech-label">utility</div>
    </div>
    <div class="srv-row">
      <button on:click={() => send('R')} disabled={!$connected}>Сбросить overrides</button>
      <button on:click={() => send('PING')} disabled={!$connected}>PING</button>
    </div>
  </section>
</div>

<style>
  .page { padding: 28px 32px 40px; max-width: 1080px; }

  .head { margin-bottom: 24px; }
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

  .warning.soft {
    background: rgba(255, 122, 24, 0.06);
    border-color: rgba(255, 122, 24, 0.25);
    color: var(--amber-soft);
  }
  .warning {
    display: inline-flex;
    align-items: center;
    gap: 8px;
    background: rgba(255, 77, 106, 0.08);
    border: 1px solid rgba(255, 77, 106, 0.3);
    border-radius: 5px;
    padding: 8px 14px;
    margin-bottom: 16px;
    color: var(--danger);
    font-size: 0.85rem;
  }

  .card {
    background: var(--bg-panel);
    border: 1px solid var(--line);
    border-radius: 8px;
    padding: 20px 24px;
    margin-bottom: 16px;
  }
  .card-head {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    margin-bottom: 16px;
  }
  h2 {
    font-family: var(--font-display);
    font-size: 1.1rem;
    font-weight: 600;
    margin: 0;
    color: var(--text-bright);
    letter-spacing: -0.01em;
  }

  /* ============ MODE TILES ============ */
  .modes {
    display: grid;
    grid-template-columns: repeat(3, 1fr);
    gap: 10px;
  }
  .mode {
    position: relative;
    display: flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 4px;
    padding: 16px 18px 18px;
    background: var(--bg-base);
    border: 1px solid var(--line);
    border-radius: 7px;
    cursor: pointer;
    color: var(--text);
    text-align: left;
    font-family: inherit;
    transition: all 200ms var(--ease-out);
    overflow: hidden;
  }
  .mode:hover:not(:disabled) { border-color: var(--line-strong); transform: translateY(-1px); }
  .mode:disabled { opacity: 0.5; cursor: not-allowed; }
  .mode.active {
    border-color: var(--amber);
    background: linear-gradient(135deg, rgba(255, 122, 24, 0.08) 0%, var(--bg-base) 60%);
  }
  .mode-key {
    font-size: 0.65rem;
    color: var(--text-faint);
    letter-spacing: 0.2em;
    font-weight: 600;
    margin-bottom: 2px;
  }
  .mode.active .mode-key { color: var(--amber); }
  .mode-label {
    font-family: var(--font-display);
    font-size: 1.2rem;
    font-weight: 600;
    color: var(--text-bright);
    letter-spacing: -0.01em;
  }
  .mode-desc {
    font-size: 0.78rem;
    color: var(--text-dim);
    line-height: 1.4;
  }
  .mode-active-bar {
    position: absolute;
    bottom: 0; left: 0;
    height: 2px; width: 100%;
    background: linear-gradient(90deg, var(--amber), transparent);
    box-shadow: 0 0 8px var(--amber-glow);
  }

  /* ============ THEMES ============ */
  .themes {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
    gap: 10px;
  }
  .theme {
    background: var(--bg-base);
    border: 1px solid var(--line);
    border-radius: 6px;
    padding: 10px;
    cursor: pointer;
    color: var(--text);
    text-align: left;
    font-family: inherit;
    transition: all 160ms var(--ease-out);
  }
  .theme:hover:not(:disabled) { border-color: var(--line-strong); transform: translateY(-1px); }
  .theme:disabled { opacity: 0.5; cursor: not-allowed; }
  .theme.active {
    border-color: var(--amber);
    box-shadow: 0 4px 16px -4px var(--amber-glow);
  }
  .swatch {
    display: flex;
    height: 40px;
    border-radius: 3px;
    overflow: hidden;
    margin-bottom: 8px;
    box-shadow: inset 0 0 0 1px rgba(0,0,0,0.4);
  }
  .swatch > div { flex: 1; }
  .theme-meta {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
  }
  .theme-name {
    font-size: 0.82rem;
    color: var(--text-bright);
    font-weight: 500;
  }
  .theme-id {
    font-size: 0.65rem;
    color: var(--text-faint);
    letter-spacing: 0.1em;
  }

  /* ============ BRIGHTNESS ============ */
  .brightness {
    display: flex;
    align-items: center;
    gap: 22px;
  }
  input[type="range"] {
    flex: 1;
    -webkit-appearance: none;
    appearance: none;
    background: transparent;
    height: 8px;
    cursor: pointer;
  }
  input[type="range"]::-webkit-slider-runnable-track {
    height: 6px;
    background: var(--bg-base);
    border: 1px solid var(--line);
    border-radius: 4px;
  }
  input[type="range"]::-webkit-slider-thumb {
    -webkit-appearance: none;
    width: 18px;
    height: 18px;
    margin-top: -7px;
    background: var(--amber);
    border: 2px solid #fff2;
    border-radius: 50%;
    cursor: grab;
    box-shadow: 0 0 12px var(--amber-glow);
    transition: transform 120ms var(--ease-out);
  }
  input[type="range"]::-webkit-slider-thumb:hover { transform: scale(1.15); }
  input[type="range"]:disabled::-webkit-slider-thumb { background: var(--text-faint); box-shadow: none; }

  .bri-value {
    display: flex;
    align-items: baseline;
    gap: 4px;
    min-width: 56px;
    justify-content: flex-end;
  }
  .bri-num {
    font-size: 1.4rem;
    font-weight: 600;
    color: var(--text-bright);
  }
  .bri-max {
    font-family: var(--font-mono);
    font-size: 0.78rem;
    color: var(--text-faint);
  }

  .service { padding-bottom: 18px; }
  .srv-row { display: flex; gap: 8px; }
  button {
    background: var(--bg-elevated);
    color: var(--text);
    border: 1px solid var(--line);
    border-radius: 5px;
    padding: 7px 14px;
    cursor: pointer;
    font-family: inherit;
    font-size: 0.85rem;
    transition: all 160ms var(--ease-out);
  }
  button:hover:not(:disabled) { border-color: var(--line-strong); background: var(--bg-hover); }
  button:disabled { opacity: 0.4; cursor: not-allowed; }
</style>
