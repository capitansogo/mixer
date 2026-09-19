<script lang="ts">
  import { onDestroy, onMount } from 'svelte';
  import { fade } from 'svelte/transition';
  import {
    Connect,
    ConfigPath,
    Disconnect,
    GetConfig,
    GetConnectionInfo,
    ListPorts,
  } from '../wailsjs/go/main/App.js';
  import { EventsOff, EventsOn } from '../wailsjs/runtime/runtime.js';
  import {
    values, connected, reconnecting, status, selectedPort, ports, configPath, cfg,
    deviceState, type Config, type DeviceState,
  } from './lib/stores';
  import Home from './pages/Home.svelte';
  import Bindings from './pages/Bindings.svelte';
  import Settings from './pages/Settings.svelte';
  import Themes from './pages/Themes.svelte';

  type Page = 'home' | 'bindings' | 'settings' | 'themes';
  let page: Page = 'home';

  const NAV: { id: Page; label: string; icon: string }[] = [
    {
      id: 'home',
      label: 'Главная',
      icon: 'M3 13h2v8H3v-8zm4-3h2v11H7V10zm4-6h2v17h-2V4zm4 3h2v14h-2V7zm4 4h2v10h-2V11z',
    },
    {
      id: 'bindings',
      label: 'Привязки',
      icon: 'M9 17H7a5 5 0 1 1 0-10h2v2H7a3 3 0 1 0 0 6h2v2zm6 0h-2v-2h2a3 3 0 1 0 0-6h-2V7h2a5 5 0 0 1 0 10zM8 11h8v2H8v-2z',
    },
    {
      id: 'themes',
      label: 'Темы LED',
      icon: 'M12 22q-2.05 0-3.875-.788t-3.187-2.15q-1.363-1.362-2.15-3.187T2 12q0-2.075.813-3.9t2.2-3.175T8.2 2.788T12.1 2q1.975 0 3.738.688t3.087 1.875t2.1 2.762t.775 3.4q0 2.9-1.75 4.588T15.5 17h-1.85q-.225 0-.312.125t-.088.275q0 .3.375.863t.375 1.287q0 1.25-.687 1.85T12 22ZM5.5 13q.65 0 1.075-.425T7 11.5t-.425-1.075T5.5 10t-1.075.425T4 11.5t.425 1.075T5.5 13Zm3-4q.65 0 1.075-.425T10 7.5t-.425-1.075T8.5 6t-1.075.425T7 7.5t.425 1.075T8.5 9Zm7 0q.65 0 1.075-.425T17 7.5t-.425-1.075T15.5 6t-1.075.425T14 7.5t.425 1.075T15.5 9Zm3 4q.65 0 1.075-.425T20 11.5t-.425-1.075T18.5 10t-1.075.425T17 11.5t.425 1.075T18.5 13Z',
    },
    {
      id: 'settings',
      label: 'Настройки',
      icon: 'M9.25 22l-.4-3.2q-.325-.125-.612-.3t-.563-.375L4.7 19.375l-2.75-4.75l2.575-1.95Q4.5 12.5 4.5 12.338v-.675q0-.163.025-.338L1.95 9.375l2.75-4.75l2.975 1.25q.275-.2.575-.375t.6-.3l.4-3.2h5.5l.4 3.2q.325.125.613.3t.562.375l2.975-1.25l2.75 4.75l-2.575 1.95q.025.175.025.338v.674q0 .163-.05.338l2.575 1.95l-2.75 4.75l-2.95-1.25q-.275.2-.575.375t-.6.3l-.4 3.2h-5.5Zm2.8-6.5q1.45 0 2.475-1.025T15.55 12q0-1.45-1.025-2.475T12.05 8.5q-1.475 0-2.488 1.025T8.55 12q0 1.45 1.013 2.475T12.05 15.5Z',
    },
  ];

  async function refreshPorts() {
    try {
      $ports = await ListPorts();
      if (!$selectedPort && $ports.length > 0) $selectedPort = $ports[0];
    } catch (e) {
      $status = `${e}`;
    }
  }

  async function connect() {
    if (!$selectedPort) {
      $status = 'Сначала выбери порт';
      return;
    }
    try {
      await Connect($selectedPort);
    } catch (e) {
      $status = `${e}`;
    }
  }

  async function disconnect() {
    await Disconnect();
    $status = 'Отключено';
    $values = [0, 0, 0, 0, 0];
  }

  // Pull the backend's view of the link. The backend also pushes
  // serial-connected / serial-disconnected events, so this is only needed
  // on mount (the auto-connect may have happened before the UI existed).
  async function syncConnection() {
    try {
      const info = await GetConnectionInfo();
      $connected = info.connected;
      $reconnecting = info.reconnecting;
      if (info.port) $selectedPort = info.port;
      if (info.connected && !$status) $status = `Подключено · ${info.port}`;
      else if (info.reconnecting) $status = `Переподключение · ${info.port}`;
    } catch {}
  }

  onMount(async () => {
    try {
      $cfg = await GetConfig();
      if ($cfg?.comPort) $selectedPort = $cfg.comPort;
    } catch {}
    try { $configPath = await ConfigPath(); } catch {}
    await refreshPorts();

    EventsOn('slider-values', (v: number[]) => { $values = v; });
    EventsOn('serial-error', (msg: string) => { $status = msg; });
    EventsOn('audio-error',  (msg: string) => { $status = `audio · ${msg}`; });
    EventsOn('serial-connected', (port: string) => {
      $connected = true;
      $reconnecting = false;
      $selectedPort = port;
      $status = `Подключено · ${port}`;
    });
    EventsOn('serial-disconnected', (reason: string) => {
      $connected = false;
      $reconnecting = false; // a following 'serial-reconnecting' flips it back
      $deviceState = null;
      $values = [0, 0, 0, 0, 0];
      $status = reason ? `Связь потеряна · ${reason}` : 'Отключено';
    });
    EventsOn('serial-reconnecting', (port: string) => {
      $reconnecting = true;
      $status = `Переподключение · ${port}`;
    });
    EventsOn('device-state', (st: DeviceState) => { $deviceState = st; });
    EventsOn('config-reloaded', (c: Config) => { $cfg = c; });

    await syncConnection();
  });

  onDestroy(() => {
    for (const ev of [
      'slider-values', 'serial-error', 'audio-error', 'serial-connected',
      'serial-disconnected', 'serial-reconnecting', 'device-state', 'config-reloaded',
    ]) EventsOff(ev);
  });
</script>

<div class="shell">
  <aside>
    <div class="brand">
      <div class="brand-mark">
        <div class="brand-bars">
          <span></span><span></span><span></span><span></span>
        </div>
      </div>
      <div class="brand-text">
        <div class="brand-name">MIXER</div>
        <div class="brand-tag">audio·hardware</div>
      </div>
    </div>

    <nav>
      {#each NAV as item}
        <button
          class:active={page === item.id}
          on:click={() => (page = item.id)}
        >
          <svg viewBox="0 0 24 24" width="18" height="18" aria-hidden="true">
            <path d={item.icon} fill="currentColor"/>
          </svg>
          <span>{item.label}</span>
          {#if page === item.id}
            <span class="nav-cursor"></span>
          {/if}
        </button>
      {/each}
    </nav>

    <div class="rail-foot">
      <div class="tech-label">device</div>
      <div class="device-line">
        <span class="dot" class:live={$connected}></span>
        <span class="device-text">
          {$connected ? $selectedPort || 'connected' : 'offline'}
        </span>
      </div>
    </div>
  </aside>

  <div class="main">
    <header>
      <div class="port-stack">
        <div class="tech-label">port</div>
        <div class="port-controls">
          <select bind:value={$selectedPort} disabled={$connected}>
            {#each $ports as p}
              <option value={p}>{p}</option>
            {/each}
            {#if !$ports.length}
              <option value="">—</option>
            {/if}
          </select>
          <button class="ghost" on:click={refreshPorts} disabled={$connected} title="Обновить">
            <svg viewBox="0 0 24 24" width="14" height="14"><path d="M17.65 6.35A7.96 7.96 0 0 0 12 4a8 8 0 0 0-7.43 11h2.13A6 6 0 0 1 18 12h-3l4 4 4-4h-3a8 8 0 0 0-2.35-5.65Z" fill="currentColor"/></svg>
          </button>
          {#if $connected}
            <button on:click={disconnect}>Отключить</button>
          {:else if $reconnecting}
            <button on:click={disconnect} title="Прекратить попытки переподключения">Отмена</button>
          {:else}
            <button class="primary" on:click={connect}>
              <span>Подключить</span>
              <svg viewBox="0 0 24 24" width="14" height="14"><path d="M13 7l5 5-5 5v-3H6v-4h7V7z" fill="currentColor"/></svg>
            </button>
          {/if}
        </div>
      </div>

      <div class="status-stack">
        <div class="tech-label">signal</div>
        <div class="status-readout" class:live={$connected}>
          <span class="signal-dot"></span>
          <span class="status-text">{$status || 'standby'}</span>
        </div>
      </div>
    </header>

    <main>
      {#key page}
        <div in:fade={{ duration: 240 }}>
          {#if page === 'home'}<Home />
          {:else if page === 'bindings'}<Bindings />
          {:else if page === 'settings'}<Settings />
          {:else if page === 'themes'}<Themes />{/if}
        </div>
      {/key}
    </main>
  </div>
</div>

<style>
  .shell {
    display: grid;
    grid-template-columns: 232px 1fr;
    height: 100vh;
  }

  /* ============ SIDEBAR ============ */
  aside {
    background: linear-gradient(180deg, #0d0d12 0%, #0a0a0e 100%);
    border-right: 1px solid var(--line);
    display: flex;
    flex-direction: column;
    padding: 1.5rem 1rem;
    gap: 1.75rem;
    position: relative;
  }
  aside::after {
    content: "";
    position: absolute;
    inset: 0 0 0 auto;
    width: 1px;
    background: linear-gradient(180deg, transparent, var(--amber-glow) 50%, transparent);
    opacity: 0.5;
  }

  .brand {
    display: flex;
    align-items: center;
    gap: 12px;
    padding: 4px 8px 14px;
    border-bottom: 1px solid var(--line-soft);
  }
  .brand-mark {
    width: 36px;
    height: 36px;
    background: linear-gradient(135deg, var(--amber) 0%, #c2410c 100%);
    border-radius: 6px;
    display: grid;
    place-items: center;
    box-shadow: 0 6px 18px -6px var(--amber-glow), inset 0 1px 0 rgba(255,255,255,0.18);
  }
  .brand-bars {
    display: flex;
    gap: 2px;
    align-items: flex-end;
    height: 16px;
  }
  .brand-bars span {
    display: block;
    width: 3px;
    background: rgba(15, 15, 18, 0.85);
    border-radius: 1px;
  }
  .brand-bars span:nth-child(1) { height: 50%; }
  .brand-bars span:nth-child(2) { height: 100%; }
  .brand-bars span:nth-child(3) { height: 70%; }
  .brand-bars span:nth-child(4) { height: 35%; }

  .brand-name {
    font-family: var(--font-display);
    font-weight: 800;
    font-size: 1.05rem;
    letter-spacing: 0.04em;
    line-height: 1;
    color: var(--text-bright);
  }
  .brand-tag {
    font-family: var(--font-mono);
    font-size: 0.62rem;
    letter-spacing: 0.16em;
    color: var(--text-dim);
    text-transform: uppercase;
    margin-top: 3px;
  }

  nav {
    display: flex;
    flex-direction: column;
    gap: 2px;
    flex: 1;
  }
  nav button {
    position: relative;
    background: transparent;
    border: none;
    color: var(--text-soft);
    text-align: left;
    padding: 10px 12px 10px 16px;
    border-radius: 6px;
    cursor: pointer;
    font-size: 0.92rem;
    font-weight: 500;
    display: flex;
    align-items: center;
    gap: 12px;
    transition: color 160ms var(--ease-out), background 160ms var(--ease-out);
  }
  nav button svg { color: var(--text-dim); transition: color 160ms var(--ease-out); }
  nav button:hover {
    background: var(--bg-hover);
    color: var(--text-bright);
  }
  nav button:hover svg { color: var(--text-soft); }
  nav button.active {
    background: linear-gradient(90deg, rgba(255, 122, 24, 0.14) 0%, transparent 100%);
    color: var(--text-bright);
  }
  nav button.active svg { color: var(--amber); }
  .nav-cursor {
    position: absolute;
    left: 0; top: 50%;
    transform: translateY(-50%);
    width: 3px;
    height: 60%;
    background: var(--amber);
    border-radius: 0 3px 3px 0;
    box-shadow: 0 0 8px var(--amber-glow);
  }

  .rail-foot {
    padding: 12px;
    border-top: 1px solid var(--line-soft);
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .device-line {
    display: flex;
    align-items: center;
    gap: 8px;
    font-family: var(--font-mono);
    font-size: 0.8rem;
  }
  .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--text-faint);
    flex-shrink: 0;
  }
  .dot.live {
    background: var(--signal);
    box-shadow: 0 0 8px var(--signal-glow);
    animation: pulse-signal 2s ease-in-out infinite;
    will-change: opacity;
  }
  .device-text { color: var(--text-soft); }

  /* ============ MAIN ============ */
  .main {
    display: flex;
    flex-direction: column;
    overflow: hidden;
    background: var(--bg-base);
  }

  header {
    display: flex;
    justify-content: space-between;
    align-items: flex-end;
    padding: 18px 32px 16px;
    gap: 32px;
    border-bottom: 1px solid var(--line);
    background: linear-gradient(180deg, var(--bg-panel) 0%, rgba(21, 21, 28, 0) 100%);
    flex-shrink: 0;
  }

  .port-stack, .status-stack {
    display: flex;
    flex-direction: column;
    gap: 6px;
  }
  .status-stack { flex: 1; min-width: 0; }

  .port-controls {
    display: flex;
    gap: 6px;
    align-items: center;
  }

  select {
    background: var(--bg-elevated);
    color: var(--text);
    border: 1px solid var(--line);
    border-radius: 5px;
    padding: 7px 10px;
    min-width: 100px;
    font-family: var(--font-mono);
    font-size: 0.85rem;
    font-weight: 500;
    transition: border-color 160ms var(--ease-out);
  }
  select:focus { outline: none; border-color: var(--amber); }
  select:disabled { opacity: 0.55; }

  header button {
    background: var(--bg-elevated);
    color: var(--text);
    border: 1px solid var(--line);
    border-radius: 5px;
    padding: 7px 14px;
    cursor: pointer;
    font-family: inherit;
    font-size: 0.85rem;
    font-weight: 500;
    display: inline-flex;
    align-items: center;
    gap: 6px;
    transition: all 160ms var(--ease-out);
  }
  header button:hover:not(:disabled) {
    border-color: var(--line-strong);
    background: var(--bg-hover);
  }
  header button:disabled { opacity: 0.4; cursor: not-allowed; }
  header button.ghost { padding: 7px 9px; color: var(--text-soft); }
  header button.primary {
    background: linear-gradient(135deg, var(--amber) 0%, #d6571c 100%);
    border-color: transparent;
    color: #1a0d04;
    font-weight: 600;
    box-shadow: 0 4px 14px -4px var(--amber-glow);
  }
  header button.primary:hover { transform: translateY(-1px); box-shadow: 0 8px 22px -6px var(--amber-glow); }

  .status-readout {
    display: inline-flex;
    align-items: center;
    gap: 10px;
    background: var(--bg-elevated);
    border: 1px solid var(--line);
    padding: 7px 14px;
    border-radius: 5px;
    color: var(--text-soft);
    font-family: var(--font-mono);
    font-size: 0.82rem;
    max-width: 100%;
    min-width: 0;
  }
  .status-readout.live { color: var(--text); border-color: rgba(74, 222, 128, 0.18); }
  .signal-dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--text-faint);
    flex-shrink: 0;
  }
  .status-readout.live .signal-dot {
    background: var(--signal);
    box-shadow: 0 0 8px var(--signal-glow);
    animation: pulse-signal 1.8s ease-in-out infinite;
    will-change: opacity;
  }
  .status-text {
    white-space: nowrap;
    overflow: hidden;
    text-overflow: ellipsis;
    flex: 1;
    min-width: 0;
  }

  main {
    flex: 1;
    overflow: auto;
  }
</style>
