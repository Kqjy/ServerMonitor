<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type PortRow, type CollectorStatus } from '$lib/api';
  import { timeAgo } from '$lib/format';

  let {
    hostId,
    collectorStatus = {}
  }: {
    hostId: number;
    sampleIntervalS?: number;
    collectorStatus?: Record<string, CollectorStatus>;
  } = $props();

  const ownerStatus = $derived(collectorStatus.connections);
  const ownersUnresolved = $derived(ownerStatus?.state === 'no_owners');
  const ownersPartial = $derived(ownerStatus?.state === 'partial_owners');
  const ownerWarning = $derived(ownersUnresolved || ownersPartial);

  let rows = $state<PortRow[]>([]);
  let loaded = $state(false);
  let error = $state<string | null>(null);
  let atMs = $state<number | null>(null);
  let stepping = $state(false);
  let atOldest = $state(false);
  let filter = $state('');
  let scope = $state<'all' | 'public' | 'local'>('all');
  let timer: ReturnType<typeof setInterval> | null = null;
  let refreshGen = 0;
  let inflight: AbortController | null = null;

  async function refresh() {
    const gen = ++refreshGen;
    inflight?.abort();
    const ac = new AbortController();
    inflight = ac;
    const opts: { at?: string; signal: AbortSignal } = { signal: ac.signal };
    if (atMs !== null) opts.at = new Date(atMs).toISOString();
    try {
      const fetched = await api.ports(hostId, opts);
      if (gen !== refreshGen) return;
      rows = fetched;
      error = null;
      if (fetched.length > 0) atOldest = false;
    } catch (e) {
      if (gen !== refreshGen || (e as { name?: string })?.name === 'AbortError') return;
      error = (e as Error).message;
    } finally {
      if (gen === refreshGen) loaded = true;
    }
  }

  async function step(dir: 'prev' | 'next') {
    if (stepping) return;
    stepping = true;
    const gen = ++refreshGen;
    inflight?.abort();
    const ac = new AbortController();
    inflight = ac;
    try {
      const boundaryMs = atMs ?? lastDataMs ?? Date.now();
      const fetched = await api.ports(hostId, {
        dir,
        at: new Date(boundaryMs).toISOString(),
        signal: ac.signal
      });
      if (gen !== refreshGen) return;
      error = null;
      if (fetched.length > 0) {
        rows = fetched;
        let max = 0;
        for (const r of fetched) {
          const t = new Date(r.time).getTime();
          if (t > max) max = t;
        }
        atMs = max > 0 ? max : null;
        atOldest = false;
      } else if (dir === 'next') {
        atMs = null;
        atOldest = false;
        await refresh();
      } else {
        atOldest = true;
      }
    } catch (e) {
      if (gen !== refreshGen || (e as { name?: string })?.name === 'AbortError') return;
      error = (e as Error).message;
    } finally {
      stepping = false;
    }
  }

  onMount(() => {
    refresh();
    timer = setInterval(() => {
      if (atMs !== null || stepping) return;
      refresh();
    }, 10_000);
  });
  onDestroy(() => {
    if (timer) clearInterval(timer);
    inflight?.abort();
  });

  function pad(n: number) {
    return String(n).padStart(2, '0');
  }
  function toLocalInputValue(ms: number): string {
    const d = new Date(ms);
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  }
  function onAtInput(e: Event) {
    const v = (e.currentTarget as HTMLInputElement).value;
    if (!v) {
      atMs = null;
      atOldest = false;
      refresh();
      return;
    }
    const d = new Date(v);
    if (isNaN(d.getTime())) return;
    atMs = d.getTime();
    atOldest = false;
    refresh();
  }
  function resetToNow() {
    atMs = null;
    atOldest = false;
    refresh();
  }

  function isLoopback(addr: string): boolean {
    return addr === '127.0.0.1' || addr === '::1' || addr.startsWith('127.');
  }
  function isWildcard(addr: string): boolean {
    return addr === '0.0.0.0' || addr === '::' || addr === '' || addr === '*';
  }
  function reachLabel(addr: string): { text: string; tone: 'public' | 'local' | 'specific' } {
    if (isLoopback(addr)) return { text: 'loopback', tone: 'local' };
    if (isWildcard(addr)) return { text: 'all interfaces', tone: 'public' };
    return { text: addr, tone: 'specific' };
  }
  function displayAddr(addr: string): string {
    if (isWildcard(addr)) return '*';
    return addr;
  }

  const lastDataMs = $derived.by(() => {
    let max = 0;
    for (const r of rows) {
      const t = new Date(r.time).getTime();
      if (t > max) max = t;
    }
    return max > 0 ? max : null;
  });
  const atLocal = $derived(
    atMs !== null
      ? toLocalInputValue(atMs)
      : lastDataMs !== null
        ? toLocalInputValue(lastDataMs)
        : ''
  );
  const isLive = $derived(atMs === null);
  const canStepPrev = $derived(!stepping && !atOldest);
  const canStepNext = $derived(!stepping && !isLive);

  const filtered = $derived.by(() => {
    const f = filter.trim().toLowerCase();
    return rows.filter((r) => {
      const loop = isLoopback(r.addr);
      if (scope === 'public' && loop) return false;
      if (scope === 'local' && !loop) return false;
      if (!f) return true;
      if (String(r.port).includes(f)) return true;
      if (r.proto.toLowerCase().includes(f)) return true;
      if (r.process && r.process.toLowerCase().includes(f)) return true;
      if (r.addr.toLowerCase().includes(f)) return true;
      return false;
    });
  });

  const counts = $derived.by(() => {
    let pub = 0;
    let loc = 0;
    let tcp = 0;
    let udp = 0;
    for (const r of rows) {
      if (isLoopback(r.addr)) loc++;
      else pub++;
      if (r.proto.startsWith('tcp')) tcp++;
      else if (r.proto.startsWith('udp')) udp++;
    }
    return { pub, loc, tcp, udp };
  });
</script>

<div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
  <header class="px-4 sm:px-5 py-3 border-b border-zinc-800 flex flex-wrap items-center justify-between gap-2">
    <div class="text-xs uppercase tracking-wider text-zinc-500">Listening ports</div>
    <div class="flex flex-wrap items-center gap-3 text-xs text-zinc-500">
      <div class="flex items-center gap-1">
        <button
          type="button"
          onclick={() => step('prev')}
          disabled={!canStepPrev}
          aria-label="Previous snapshot"
          title="Previous snapshot"
          class="p-1 rounded-md border border-zinc-800 text-zinc-300 hover:bg-zinc-800/40 disabled:opacity-40 disabled:cursor-default transition-colors">
          <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="15 18 9 12 15 6" /></svg>
        </button>
        <input
          type="datetime-local"
          step="1"
          value={atLocal}
          oninput={onAtInput}
          aria-label="Snapshot timestamp"
          class="bg-zinc-950 border border-zinc-800 rounded px-2 py-1 text-xs numeric" />
        <button
          type="button"
          onclick={() => step('next')}
          disabled={!canStepNext}
          aria-label="Next snapshot"
          title="Next snapshot"
          class="p-1 rounded-md border border-zinc-800 text-zinc-300 hover:bg-zinc-800/40 disabled:opacity-40 disabled:cursor-default transition-colors">
          <svg viewBox="0 0 24 24" class="h-3.5 w-3.5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6" /></svg>
        </button>
        <button
          type="button"
          onclick={resetToNow}
          disabled={isLive}
          class="ml-1 px-2 py-1 rounded-md border border-zinc-800 text-zinc-300 hover:bg-zinc-800/40 disabled:opacity-40 disabled:cursor-default transition-colors">
          Now
        </button>
      </div>
      <span class="numeric text-zinc-400">
        {counts.pub} public · {counts.loc} loopback · {counts.tcp} tcp · {counts.udp} udp
      </span>
    </div>
  </header>

  <div class="px-4 sm:px-5 py-2 border-b border-zinc-800 flex flex-wrap items-center gap-2">
    <input
      type="text"
      placeholder="Filter port, process, or address…"
      bind:value={filter}
      class="flex-1 min-w-[12rem] bg-zinc-950 border border-zinc-800 rounded px-2 py-1 text-xs"
    />
    <div class="flex items-center gap-1 text-xs">
      {#each [{v:'all',l:'All'},{v:'public',l:'Public'},{v:'local',l:'Loopback'}] as opt (opt.v)}
        <button
          type="button"
          onclick={() => (scope = opt.v as typeof scope)}
          class="px-2 py-1 rounded-md transition-colors {scope === opt.v ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">
          {opt.l}
        </button>
      {/each}
    </div>
  </div>

  {#if ownerWarning}
    <div class="px-4 sm:px-5 py-3 border-b border-amber-900/40 bg-amber-950/20 text-xs text-amber-100/90 space-y-1">
      <p>{ownersPartial
        ? 'The agent could map only some listening sockets to their owning processes; the Process and PID columns are blank for the rest. Port numbers and bindings below are still accurate.'
        : 'The agent could not map listening sockets to their owning processes, so the Process and PID columns are blank. Port numbers and bindings below are still accurate.'}</p>
      <p class="text-amber-100/70">On Linux this means the agent lacks <span class="font-mono">CAP_SYS_PTRACE</span>, or in a container is blocked by Docker's default AppArmor profile.</p>
      {#if ownerStatus?.message}
        <p class="text-amber-100/60 text-[11px] font-mono pt-0.5">{ownerStatus.message}</p>
      {/if}
    </div>
  {/if}

  {#if error}
    <div class="px-4 sm:px-5 py-3 border-b border-rose-900/40 bg-rose-950/30 text-sm text-rose-300">
      Failed to load ports: {error}
    </div>
  {/if}

  {#if !loaded}
    <div class="p-4">
      <div class="h-40 rounded-lg shimmer"></div>
    </div>
  {:else if rows.length === 0 && !error}
    <div class="p-12 text-center">
      <div class="mx-auto h-10 w-10 rounded-lg bg-zinc-800/70 grid place-items-center mb-4">
        <svg viewBox="0 0 24 24" class="h-5 w-5 text-zinc-400" fill="none" stroke="currentColor" stroke-width="1.6">
          <rect x="3" y="11" width="18" height="10" rx="2" /><path d="M7 11V7a5 5 0 0 1 10 0v4" />
        </svg>
      </div>
      <h2 class="text-base font-medium text-zinc-100">No listening ports</h2>
      <p class="mt-1 text-sm text-zinc-500 max-w-md mx-auto">
        {isLive
          ? 'The agent did not report any bound sockets on this host.'
          : 'No port data within 2 minutes of the selected moment.'}
      </p>
    </div>
  {:else if rows.length > 0}
    <div class="overflow-x-auto">
      <table class="w-full text-sm">
        <thead class="text-[10px] uppercase tracking-wider text-zinc-500 bg-zinc-900/60">
          <tr>
            <th class="text-right font-medium px-5 py-2.5 w-20">Port</th>
            <th class="text-left font-medium px-3 py-2.5 w-20">Proto</th>
            <th class="text-left font-medium px-3 py-2.5">Bind</th>
            <th class="text-left font-medium px-3 py-2.5">Process</th>
            <th class="text-right font-medium px-3 py-2.5">PID</th>
            <th class="text-right font-medium px-5 py-2.5">Updated</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-zinc-800/70">
          {#each filtered as p (p.proto + '|' + p.addr + '|' + p.port + '|' + (p.pid ?? 0))}
            {@const reach = reachLabel(p.addr)}
            <tr class="hover:bg-zinc-900/60">
              <td class="px-5 py-2 text-right numeric font-mono text-zinc-100">{p.port}</td>
              <td class="px-3 py-2 text-zinc-400 text-xs font-mono uppercase">{p.proto}</td>
              <td class="px-3 py-2 text-xs">
                <div class="flex items-center gap-2">
                  <span class="font-mono text-zinc-400">{displayAddr(p.addr)}</span>
                  {#if reach.tone === 'public'}
                    <span class="text-[10px] uppercase tracking-wider text-amber-400">all interfaces</span>
                  {:else if reach.tone === 'local'}
                    <span class="text-[10px] uppercase tracking-wider text-zinc-500">loopback</span>
                  {/if}
                </div>
              </td>
              <td class="px-3 py-2 text-zinc-300 text-xs truncate max-w-xs">{p.process || '—'}</td>
              <td class="px-3 py-2 text-right numeric text-zinc-500 text-xs">{p.pid && p.pid > 0 ? p.pid : '—'}</td>
              <td class="px-5 py-2 text-right text-zinc-500 text-xs numeric">{timeAgo(p.time)}</td>
            </tr>
          {/each}
          {#if filtered.length === 0}
            <tr>
              <td colspan="6" class="px-5 py-8 text-center text-xs text-zinc-500">
                No ports match the current filter.
              </td>
            </tr>
          {/if}
        </tbody>
      </table>
    </div>
  {/if}
</div>
