<script lang="ts">
  import { onMount } from 'svelte';
  import { cfg, status, type AudioSession } from '../lib/stores';
  import {
    ListAudioSessions,
    SaveConfig,
  } from '../../wailsjs/go/main/App.js';

  let sessions: AudioSession[] = [];
  let loading = false;
  let addingTo: number | null = null;
  let customExe = '';

  const NUM_SLIDERS = 5;
  const SPECIAL = [
    { id: 'master', label: 'Master',     desc: 'Общий уровень Windows' },
    { id: 'system', label: 'System',     desc: 'Звуки системы' },
    { id: 'mic',    label: 'Mic',        desc: 'Микрофон (устройство записи по умолчанию)' },
    { id: 'game',   label: 'Foreground', desc: 'Активное окно' },
  ];

  $: mapping = $cfg?.sliderMapping ?? {};

  async function refresh() {
    loading = true;
    try {
      sessions = await ListAudioSessions();
    } catch (e) {
      $status = `${e}`;
    }
    loading = false;
  }

  async function save() {
    if (!$cfg) return;
    try {
      await SaveConfig($cfg);
      $status = 'Привязки сохранены';
    } catch (e) {
      $status = `${e}`;
    }
  }

  function ensureSlider(i: number) {
    if (!$cfg) return;
    const m = { ...$cfg.sliderMapping };
    if (!m[i]) m[i] = [];
    $cfg = { ...$cfg, sliderMapping: m };
  }

  function addTarget(slider: number, target: string) {
    if (!$cfg || !target) return;
    ensureSlider(slider);
    const list = $cfg.sliderMapping[slider] ?? [];
    if (list.includes(target)) return;
    const m = { ...$cfg.sliderMapping, [slider]: [...list, target] };
    $cfg = { ...$cfg, sliderMapping: m };
    addingTo = null;
    customExe = '';
    save();
  }

  function removeTarget(slider: number, target: string) {
    if (!$cfg) return;
    const list = ($cfg.sliderMapping[slider] ?? []).filter((t) => t !== target);
    const m = { ...$cfg.sliderMapping, [slider]: list };
    $cfg = { ...$cfg, sliderMapping: m };
    save();
  }

  function addCustom(slider: number) {
    const v = customExe.trim().toLowerCase();
    if (v) addTarget(slider, v);
  }

  onMount(refresh);
</script>

<div class="page">
  <header class="head">
    <div>
      <div class="kicker tech-label">routing matrix</div>
      <h1>Привязки</h1>
      <p class="sub">Какое приложение управляется каким ползунком.</p>
    </div>
    <button class="ghost" on:click={refresh} disabled={loading}>
      <svg viewBox="0 0 24 24" width="14" height="14"><path d="M17.65 6.35A7.96 7.96 0 0 0 12 4a8 8 0 0 0-7.43 11h2.13A6 6 0 0 1 18 12h-3l4 4 4-4h-3a8 8 0 0 0-2.35-5.65Z" fill="currentColor"/></svg>
      <span>{loading ? 'Обновляю…' : 'Обновить сессии'}</span>
    </button>
  </header>

  <div class="matrix">
    {#each Array(NUM_SLIDERS) as _, i}
      <div class="row">
        <div class="row-id">
          <div class="lamp" class:on={(mapping[i] ?? []).length > 0}></div>
          <div class="row-num">{String(i + 1).padStart(2, '0')}</div>
        </div>

        <div class="patch">
          {#each (mapping[i] ?? []) as t}
            <span class="patch-chip" class:special={SPECIAL.some(s => s.id === t)}>
              <span class="dot"></span>
              <span class="patch-name">{t}</span>
              <button class="patch-x" on:click={() => removeTarget(i, t)} title="Убрать">
                <svg viewBox="0 0 24 24" width="11" height="11"><path d="M19 6.41 17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z" fill="currentColor"/></svg>
              </button>
            </span>
          {/each}
          {#if !(mapping[i]?.length)}
            <span class="empty-line">— нет привязок —</span>
          {/if}
          <button class="add" on:click={() => (addingTo = addingTo === i ? null : i)}>
            <svg viewBox="0 0 24 24" width="12" height="12"><path d="M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z" fill="currentColor"/></svg>
            <span>Добавить</span>
          </button>
        </div>

        {#if addingTo === i}
          <div class="picker">
            <div class="pick-section">
              <div class="tech-label">Специальные цели</div>
              <div class="pick-row">
                {#each SPECIAL as s}
                  <button class="pick-btn special" on:click={() => addTarget(i, s.id)}>
                    <span class="pb-label">{s.label}</span>
                    <span class="pb-desc">{s.desc}</span>
                  </button>
                {/each}
              </div>
            </div>

            <div class="pick-section">
              <div class="tech-label">Запущенные сессии</div>
              <div class="pick-row">
                {#if sessions.length === 0}
                  <span class="muted">пусто — жми «Обновить сессии»</span>
                {/if}
                {#each sessions.filter(s => s.name) as s}
                  <button class="pick-btn" on:click={() => addTarget(i, s.name)}>
                    <span class="pb-label">{s.name}</span>
                    <span class="pb-desc">pid {s.pid}</span>
                  </button>
                {/each}
              </div>
            </div>

            <div class="pick-section">
              <div class="tech-label">Вручную</div>
              <div class="pick-row">
                <input
                  type="text"
                  placeholder="something.exe"
                  bind:value={customExe}
                  on:keydown={(e) => e.key === 'Enter' && addCustom(i)}
                />
                <button class="pick-btn" on:click={() => addCustom(i)}>Добавить</button>
              </div>
            </div>
          </div>
        {/if}
      </div>
    {/each}
  </div>
</div>

<style>
  .page { padding: 28px 32px 32px; }

  .head {
    display: flex;
    justify-content: space-between;
    align-items: flex-end;
    gap: 24px;
    margin-bottom: 24px;
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
  .sub {
    color: var(--text-dim);
    margin: 8px 0 0;
    font-size: 0.9rem;
  }

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
  button:disabled { opacity: 0.4; cursor: not-allowed; }
  button.ghost { background: transparent; }

  .matrix {
    display: flex;
    flex-direction: column;
    gap: 10px;
  }

  .row {
    display: grid;
    grid-template-columns: 64px 1fr;
    gap: 16px;
    align-items: start;
    padding: 14px 20px;
    background: var(--bg-panel);
    border: 1px solid var(--line);
    border-radius: 8px;
    position: relative;
    transition: border-color 160ms var(--ease-out);
  }
  .row:hover { border-color: var(--line-strong); }

  .row-id {
    display: flex;
    align-items: center;
    gap: 10px;
    padding-top: 4px;
  }
  .lamp {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: var(--text-faint);
    box-shadow: inset 0 0 2px rgba(0,0,0,0.5);
  }
  .lamp.on {
    background: var(--amber);
    box-shadow: 0 0 6px var(--amber-glow), inset 0 0 2px rgba(255,255,255,0.4);
  }
  .row-num {
    font-family: var(--font-mono);
    font-weight: 600;
    font-size: 1.05rem;
    color: var(--text-bright);
    letter-spacing: 0.02em;
  }

  .patch {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
    padding-top: 4px;
  }
  .patch-chip {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    background: rgba(255, 122, 24, 0.08);
    border: 1px solid rgba(255, 122, 24, 0.24);
    color: var(--amber-soft);
    border-radius: 4px;
    padding: 3px 4px 3px 8px;
    font-family: var(--font-mono);
    font-size: 0.78rem;
    transition: border-color 160ms var(--ease-out);
  }
  .patch-chip.special {
    background: rgba(111, 182, 255, 0.08);
    border-color: rgba(111, 182, 255, 0.28);
    color: var(--info);
  }
  .patch-chip:hover { border-color: rgba(255, 122, 24, 0.5); }
  .patch-chip .dot {
    width: 5px;
    height: 5px;
    border-radius: 50%;
    background: currentColor;
    box-shadow: 0 0 4px currentColor;
  }
  .patch-x {
    background: transparent;
    border: none;
    color: var(--text-dim);
    padding: 0 4px;
    display: inline-flex;
    align-items: center;
  }
  .patch-x:hover { color: var(--danger); background: transparent; }

  .empty-line {
    color: var(--text-faint);
    font-family: var(--font-mono);
    font-size: 0.75rem;
    letter-spacing: 0.05em;
  }

  .add {
    background: transparent;
    border: 1px dashed var(--line-strong);
    color: var(--text-dim);
    padding: 3px 9px;
    font-size: 0.75rem;
    font-family: var(--font-mono);
  }
  .add:hover { color: var(--amber); border-color: var(--amber); background: transparent; }

  .picker {
    grid-column: 1 / -1;
    margin-top: 12px;
    padding: 16px;
    background: var(--bg-base);
    border: 1px solid var(--line);
    border-radius: 6px;
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .pick-section { display: flex; flex-direction: column; gap: 8px; }
  .pick-row {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
  }
  .pick-btn {
    display: inline-flex;
    flex-direction: column;
    align-items: flex-start;
    gap: 2px;
    background: var(--bg-elevated);
    border: 1px solid var(--line);
    color: var(--text);
    padding: 7px 12px;
    border-radius: 4px;
    font-size: 0.8rem;
    cursor: pointer;
  }
  .pick-btn:hover { border-color: var(--amber); }
  .pick-btn.special:hover { border-color: var(--info); }
  .pb-label { font-family: var(--font-mono); font-weight: 500; }
  .pb-desc {
    font-size: 0.65rem;
    color: var(--text-dim);
    letter-spacing: 0.05em;
  }

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
</style>
