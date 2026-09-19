<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { cfg, connected, ports, status, configPath } from '../lib/stores';
  import {
    CancelCalibration,
    GetAutostart,
    ListPorts,
    ResetCalibration,
    SaveConfig,
    SetAutostart,
    StartCalibration,
    StopCalibration,
  } from '../../wailsjs/go/main/App.js';
  import { EventsOff, EventsOn } from '../../wailsjs/runtime/runtime.js';

  type CalRow = { min: number; max: number };

  let calibrating = false;
  let calLive: CalRow[] = [{min: 0, max: 0}, {min: 0, max: 0}, {min: 0, max: 0}, {min: 0, max: 0}, {min: 0, max: 0}];
  let autostart = false;

  $: storedCal = $cfg?.calibration ?? {};

  async function refreshPorts() {
    try { $ports = await ListPorts(); } catch (e) { $status = `${e}`; }
  }

  async function save() {
    if (!$cfg) return;
    try { await SaveConfig($cfg); $status = 'Настройки сохранены'; }
    catch (e) { $status = `${e}`; }
  }

  async function toggleAutostart(e: Event) {
    const t = e.target as HTMLInputElement;
    try {
      await SetAutostart(t.checked);
      autostart = t.checked;
      $status = autostart ? 'Автозапуск включён' : 'Автозапуск выключен';
    } catch (err) {
      t.checked = autostart;
      $status = `${err}`;
    }
  }

  async function startCal() {
    try {
      await StartCalibration();
      calibrating = true;
      calLive = calLive.map(() => ({min: 9999, max: -1}));
      $status = 'Калибровка: подвигай ползунки до упора в обе стороны';
    } catch (e) { $status = `${e}`; }
  }

  async function stopCal() {
    try {
      const saved = await StopCalibration();
      calibrating = false;
      if ($cfg) $cfg = { ...$cfg, calibration: saved };
      $status = 'Калибровка сохранена';
    } catch (e) { $status = `${e}`; }
  }

  async function cancelCal() {
    await CancelCalibration();
    calibrating = false;
    $status = 'Калибровка отменена';
  }

  async function resetCal() {
    try {
      await ResetCalibration();
      if ($cfg) {
        const def: Record<number, CalRow> = {};
        for (let i = 0; i < 5; i++) def[i] = { min: 0, max: 1023 };
        $cfg = { ...$cfg, calibration: def };
      }
      $status = 'Калибровка сброшена в 0..1023';
    } catch (e) { $status = `${e}`; }
  }

  onMount(async () => {
    EventsOn('calibration-progress', (rows: CalRow[]) => { calLive = rows; });
    try { autostart = await GetAutostart(); } catch {}
  });

  onDestroy(() => {
    EventsOff('calibration-progress');
    if (calibrating) cancelCal();
  });
</script>

<div class="page">
  <header class="head">
    <div>
      <div class="kicker tech-label">control panel</div>
      <h1>Настройки</h1>
      <p class="sub">Параметры подключения, обработки сигнала и калибровка.</p>
    </div>
  </header>

  {#if $cfg}
    <section class="card">
      <div class="card-head">
        <h2>Соединение</h2>
        <div class="tech-label">connection</div>
      </div>

      <div class="form">
        <div class="field">
          <div class="field-label">Порт по умолчанию (auto-connect)</div>
          <div class="row">
            <select bind:value={$cfg.comPort}>
              <option value="">— не подключаться автоматически —</option>
              {#each $ports as p}<option value={p}>{p}</option>{/each}
            </select>
            <button on:click={refreshPorts} class="ghost" title="Обновить список">↻</button>
          </div>
        </div>

        <div class="field">
          <div class="field-label">Скорость порта (baud)</div>
          <input type="number" bind:value={$cfg.baudRate} min="9600" step="1" />
          <div class="hint">По умолчанию 115200 — совпадает с прошивкой.</div>
        </div>
      </div>
    </section>

    <section class="card">
      <div class="card-head">
        <h2>Сигнал</h2>
        <div class="tech-label">signal processing</div>
      </div>

      <div class="form">
        <label class="toggle">
          <input type="checkbox" bind:checked={$cfg.invertSliders} />
          <span class="track"><span class="thumb"></span></span>
          <span class="toggle-text">
            <span class="toggle-title">Инвертировать ползунки</span>
            <span class="toggle-desc">Нижнее положение = 100%</span>
          </span>
        </label>

        <div class="field">
          <div class="field-label">Зона нечувствительности (deadzone)</div>
          <input type="number" bind:value={$cfg.noiseReduction} min="0" max="50" step="1" />
          <div class="hint">В единицах АЦП 0..1023. Значения ниже порога обнуляются.</div>
        </div>
      </div>
    </section>

    <section class="card">
      <div class="card-head">
        <h2>Запуск</h2>
        <div class="tech-label">startup</div>
      </div>

      <div class="form">
        <label class="toggle">
          <input type="checkbox" checked={autostart} on:change={toggleAutostart} />
          <span class="track"><span class="thumb"></span></span>
          <span class="toggle-text">
            <span class="toggle-title">Запускать при входе в Windows</span>
            <span class="toggle-desc">Стартует сразу в трей, окно не показывается</span>
          </span>
        </label>
      </div>
    </section>

    <section class="card actions-card">
      <button class="primary" on:click={save}>
        <svg viewBox="0 0 24 24" width="14" height="14"><path d="M17 3H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V7l-4-4zm-5 16a3 3 0 1 1 0-6 3 3 0 0 1 0 6zm3-10H5V5h10v4z" fill="currentColor"/></svg>
        Сохранить
      </button>
      <div class="path-line">
        <span class="tech-label">config</span>
        <code>{$configPath || 'не определён'}</code>
      </div>
    </section>

    <section class="card">
      <div class="card-head">
        <h2>Калибровка ползунков</h2>
        <div class="tech-label">calibration</div>
      </div>
      <p class="sub">
        Если ползунок не достаёт до 0 или 1023 (типично для ESP32 ADC), калибровка запомнит
        фактические крайние положения, чтобы 0% и 100% соответствовали физическому ходу.
      </p>

      {#if !calibrating}
        <div class="cal-actions">
          <button class="primary" on:click={startCal} disabled={!$connected}>Начать калибровку</button>
          <button on:click={resetCal}>Сбросить в 0..1023</button>
        </div>
        <div class="cal-grid">
          {#each Array(5) as _, i}
            {@const c = storedCal[i] ?? { min: 0, max: 1023 }}
            <div class="cal-cell">
              <div class="cal-num tech-data">{String(i + 1).padStart(2, '0')}</div>
              <div class="cal-stats">
                <div class="cal-stat"><span class="tech-label">min</span><span class="cal-v">{c.min}</span></div>
                <div class="cal-stat"><span class="tech-label">max</span><span class="cal-v">{c.max}</span></div>
                <div class="cal-stat"><span class="tech-label">range</span><span class="cal-v">{c.max - c.min}</span></div>
              </div>
            </div>
          {/each}
        </div>
      {:else}
        <div class="warning">
          Подвигай каждый ползунок вверх и вниз до упора. Когда обойдёшь все — нажми «Готово».
        </div>
        <div class="cal-grid">
          {#each calLive as c, i}
            <div class="cal-cell live">
              <div class="cal-num tech-data">{String(i + 1).padStart(2, '0')}</div>
              <div class="cal-stats">
                <div class="cal-stat"><span class="tech-label">min</span><span class="cal-v">{c.min === 9999 ? '—' : c.min}</span></div>
                <div class="cal-stat"><span class="tech-label">max</span><span class="cal-v">{c.max === -1 ? '—' : c.max}</span></div>
              </div>
            </div>
          {/each}
        </div>
        <div class="cal-actions">
          <button class="primary" on:click={stopCal}>Готово</button>
          <button on:click={cancelCal}>Отмена</button>
        </div>
      {/if}
    </section>
  {:else}
    <p class="muted">Конфиг ещё не загружен…</p>
  {/if}
</div>

<style>
  .page { padding: 28px 32px 40px; max-width: 920px; }

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

  .form {
    display: flex;
    flex-direction: column;
    gap: 16px;
  }
  .field { display: flex; flex-direction: column; gap: 6px; }
  .field-label { font-size: 0.82rem; color: var(--text-soft); }
  .hint { font-size: 0.72rem; color: var(--text-faint); }
  .row { display: flex; gap: 6px; }

  select, input[type="number"] {
    background: var(--bg-base);
    border: 1px solid var(--line);
    color: var(--text);
    border-radius: 5px;
    padding: 7px 10px;
    font-family: var(--font-mono);
    font-size: 0.88rem;
    width: 100%;
  }
  select:focus, input:focus { outline: none; border-color: var(--amber); }

  button {
    background: var(--bg-elevated);
    color: var(--text);
    border: 1px solid var(--line);
    border-radius: 5px;
    padding: 7px 14px;
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
  button.ghost { background: transparent; padding: 7px 11px; }
  button.primary {
    background: linear-gradient(135deg, var(--amber) 0%, #d6571c 100%);
    border-color: transparent;
    color: #1a0d04;
    font-weight: 600;
    box-shadow: 0 4px 14px -4px var(--amber-glow);
  }
  button.primary:hover { transform: translateY(-1px); box-shadow: 0 8px 22px -6px var(--amber-glow); }

  /* ============ toggle ============ */
  .toggle {
    display: flex;
    align-items: center;
    gap: 14px;
    cursor: pointer;
    padding: 4px 0;
  }
  .toggle input { display: none; }
  .track {
    width: 38px;
    height: 22px;
    background: var(--bg-elevated);
    border: 1px solid var(--line);
    border-radius: 11px;
    position: relative;
    flex-shrink: 0;
    transition: all 200ms var(--ease-out);
  }
  .thumb {
    position: absolute;
    top: 2px; left: 2px;
    width: 16px;
    height: 16px;
    border-radius: 50%;
    background: var(--text-dim);
    transition: all 200ms var(--ease-spring);
    box-shadow: 0 1px 3px rgba(0,0,0,0.3);
  }
  .toggle input:checked + .track {
    background: rgba(255, 122, 24, 0.2);
    border-color: var(--amber);
  }
  .toggle input:checked + .track .thumb {
    left: 18px;
    background: var(--amber);
    box-shadow: 0 0 8px var(--amber-glow);
  }
  .toggle-text { display: flex; flex-direction: column; gap: 1px; }
  .toggle-title { color: var(--text); font-size: 0.92rem; }
  .toggle-desc { color: var(--text-dim); font-size: 0.74rem; }

  /* ============ actions-card ============ */
  .actions-card {
    display: flex;
    justify-content: space-between;
    align-items: center;
    gap: 16px;
    padding: 14px 20px;
  }
  .path-line {
    display: flex;
    align-items: center;
    gap: 10px;
    overflow: hidden;
  }
  .path-line code {
    font-family: var(--font-mono);
    font-size: 0.75rem;
    color: var(--text-soft);
    background: var(--bg-base);
    padding: 4px 10px;
    border-radius: 4px;
    border: 1px solid var(--line);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 420px;
  }

  /* ============ calibration ============ */
  .cal-actions { display: flex; gap: 8px; margin: 16px 0 14px; }

  .warning {
    background: rgba(255, 122, 24, 0.08);
    border: 1px solid rgba(255, 122, 24, 0.3);
    border-radius: 5px;
    padding: 10px 14px;
    margin: 14px 0;
    color: var(--amber-soft);
    font-size: 0.85rem;
  }

  .cal-grid {
    display: grid;
    grid-template-columns: repeat(5, 1fr);
    gap: 10px;
  }
  .cal-cell {
    background: var(--bg-base);
    border: 1px solid var(--line);
    border-radius: 6px;
    padding: 12px 10px;
    display: flex;
    flex-direction: column;
    gap: 8px;
    align-items: center;
  }
  .cal-cell.live { border-color: rgba(255, 122, 24, 0.4); }
  .cal-num {
    font-weight: 600;
    font-size: 1.1rem;
    color: var(--text-bright);
  }
  .cal-stats {
    display: flex;
    flex-direction: column;
    gap: 4px;
    width: 100%;
  }
  .cal-stat {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
  }
  .cal-v {
    font-family: var(--font-mono);
    font-size: 0.82rem;
    color: var(--text-bright);
    font-variant-numeric: tabular-nums;
  }

  .muted { color: var(--text-dim); }
</style>
