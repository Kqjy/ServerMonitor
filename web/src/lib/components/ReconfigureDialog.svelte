<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import { api, type Host } from '$lib/api';
  import { modalFocus } from '$lib/modal';

  let { host, onclose }: { host: Host; onclose: () => void } = $props();

  const cs = untrack(() => host.collector_status ?? {});
  const smartState = cs.smart?.state;
  const smartFailing =
    smartState === 'read_failed' || smartState === 'scan_failed' || smartState === 'binary_missing';
  const ownerState = cs.connections?.state ?? cs.ports?.state;
  const ownersMissing = ownerState === 'no_owners' || ownerState === 'partial_owners';
  const ownersWorking = ownerState === 'ok';
  const smartWorking = smartState === 'ok';

  const osName = untrack(() => (host.os ?? '').toLowerCase());
  const isWindows = osName.includes('windows');
  const isLinux = osName.includes('linux');

  let enablePortOwners = $state(untrack(() => ownersMissing || ownersWorking));
  let enableDocker = $state(false);
  let enableSmart = $state(untrack(() => smartFailing || smartWorking));
  let enableSmartNvme = $state(untrack(() => smartState === 'read_failed'));
  let enableGpu = $state(false);
  let enableNetwork = $state(false);
  let adminService = $state(untrack(() => smartFailing || smartWorking));

  let baseUrl = $state(typeof window !== 'undefined' ? window.location.origin : '');
  let copied = $state(false);

  onMount(async () => {
    try {
      const info = await api.serverInfo();
      if (info?.url) baseUrl = info.url;
    } catch {}
  });

  const shellOneLiner = $derived.by(() => {
    const vars: string[] = [];
    if (enablePortOwners) vars.push('SM_ENABLE_PORT_OWNERS=1');
    if (enableDocker) vars.push('SM_ENABLE_DOCKER=1');
    if (enableSmart) vars.push('SM_ENABLE_SMART=1');
    if (enableSmart && enableSmartNvme) vars.push('SM_ENABLE_SMART_NVME=1');
    if (enableGpu) vars.push('SM_ENABLE_GPU=1');
    if (enableNetwork) vars.push('SM_ENABLE_NETWORK=1');
    const run = `bash -c "curl -fsSL ${baseUrl}/install.sh | bash"`;
    if (vars.length === 0) return `sudo ${run}`;
    const preserve = vars.map((v) => v.split('=')[0]).join(',');
    return `${vars.join(' ')} sudo --preserve-env=${preserve} ${run}`;
  });

  const pwshOneLiner = $derived.by(() => {
    const parts: string[] = [];
    if (adminService) parts.push('$env:SM_ADMIN_SERVICE="1"');
    if (enableSmart) parts.push('$env:SM_ENABLE_SMART="1"');
    const iex = `iex (iwr -useb ${baseUrl}/install.ps1).Content`;
    return parts.length ? `${parts.join('; ')}; ${iex}` : iex;
  });

  const command = $derived(isWindows ? pwshOneLiner : shellOneLiner);

  let copyFailed = $state(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(command);
      copied = true;
    } catch {
      copyFailed = true;
    }
    setTimeout(() => {
      copied = false;
      copyFailed = false;
    }, 1500);
  }
</script>

<div
  role="dialog"
  aria-modal="true"
  tabindex="-1"
  use:modalFocus
  class="fixed inset-0 z-40 bg-zinc-950/60 backdrop-blur-sm flex items-center justify-center p-4"
  onclick={(e) => {
    if (e.target === e.currentTarget) onclose();
  }}
  onkeydown={(e) => {
    if (e.key === 'Escape') onclose();
  }}
>
  <div class="w-full max-w-lg rounded-xl border border-zinc-800 bg-zinc-900 shadow-2xl overflow-hidden">
    <header class="px-5 py-3 border-b border-zinc-800">
      <h2 class="text-sm font-medium text-zinc-100">Reconfigure agent</h2>
      <p class="mt-1 text-[11px] text-zinc-500">
        Run this on <span class="font-mono text-zinc-400">{host.hostname}</span>. The installer detects the existing
        agent and restarts it with the chosen capabilities — no token, no re-registration, identity and binary
        unchanged.
      </p>
    </header>

    <div class="px-5 py-4 space-y-4">
      {#if isLinux}
        <div class="space-y-2">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Capabilities</div>
          <label class="flex items-start gap-2 text-xs text-zinc-300 cursor-pointer select-none">
            <input type="checkbox" bind:checked={enablePortOwners} class="mt-0.5 accent-emerald-500" />
            <span><span class="text-zinc-100">Listening-port owners</span> <span class="text-zinc-500">— grants <span class="font-mono">CAP_DAC_READ_SEARCH</span> + <span class="font-mono">CAP_SYS_PTRACE</span> to map ports to PIDs; lets the agent read other processes' memory and environment (secrets)</span></span>
          </label>
          <label class="flex items-start gap-2 text-xs text-zinc-300 cursor-pointer select-none">
            <input type="checkbox" bind:checked={enableDocker} class="mt-0.5 accent-emerald-500" />
            <span><span class="text-zinc-100">Docker containers</span> <span class="text-zinc-500">— joins <span class="font-mono">docker</span> group (effectively root on host)</span></span>
          </label>
          <label class="flex items-start gap-2 text-xs text-zinc-300 cursor-pointer select-none">
            <input type="checkbox" bind:checked={enableSmart} class="mt-0.5 accent-emerald-500" />
            <span><span class="text-zinc-100">Disk SMART</span> <span class="text-zinc-500">— joins <span class="font-mono">disk</span> group, grants <span class="font-mono">CAP_SYS_RAWIO</span>, auto-installs <span class="font-mono">smartmontools</span>. Covers SATA/SAS only</span></span>
          </label>
          {#if enableSmart}
            <label class="flex items-start gap-2 text-xs text-zinc-300 cursor-pointer select-none ml-6">
              <input type="checkbox" bind:checked={enableSmartNvme} class="mt-0.5 accent-amber-500" />
              <span><span class="text-zinc-100">Include NVMe drives</span> <span class="text-zinc-500">— additionally grants <span class="font-mono">CAP_SYS_ADMIN</span> (NVMe SMART needs it; <span class="font-mono">CAP_SYS_RAWIO</span> does not cover NVMe). <span class="text-amber-300/80">Near-root — enable only where NVMe SMART is worth the exposure.</span></span></span>
            </label>
          {/if}
          <label class="flex items-start gap-2 text-xs text-zinc-300 cursor-pointer select-none">
            <input type="checkbox" bind:checked={enableGpu} class="mt-0.5 accent-emerald-500" />
            <span><span class="text-zinc-100">GPU (nvidia)</span> <span class="text-zinc-500">— joins <span class="font-mono">video</span> group</span></span>
          </label>
          <label class="flex items-start gap-2 text-xs text-zinc-300 cursor-pointer select-none">
            <input type="checkbox" bind:checked={enableNetwork} class="mt-0.5 accent-emerald-500" />
            <span><span class="text-zinc-100">Privileged network</span> <span class="text-zinc-500">— grants <span class="font-mono">CAP_NET_ADMIN</span> + <span class="font-mono">CAP_NET_RAW</span></span></span>
          </label>
        </div>
      {:else if isWindows}
        <div class="space-y-2">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Capabilities</div>
          <label class="flex items-start gap-2 text-xs text-zinc-300 cursor-pointer select-none">
            <input type="checkbox" bind:checked={adminService} class="mt-0.5 accent-emerald-500" />
            <span><span class="text-zinc-100">Admin service</span> <span class="text-zinc-500">— runs as <span class="font-mono">LocalSystem</span> (needed for SMART and full process visibility)</span></span>
          </label>
          <label class="flex items-start gap-2 text-xs text-zinc-300 cursor-pointer select-none">
            <input type="checkbox" bind:checked={enableSmart} class="mt-0.5 accent-emerald-500" />
            <span><span class="text-zinc-100">Disk SMART</span> <span class="text-zinc-500">— auto-installs <span class="font-mono">smartmontools</span> via <span class="font-mono">winget</span> (also needs Admin service). NVMe works under the Admin service with no extra grant</span></span>
          </label>
        </div>
      {:else}
        <p class="text-xs text-zinc-400">Script reconfigure is available for Linux and Windows hosts. This host reports <span class="font-mono text-zinc-300">{host.os || 'an unknown OS'}</span>.</p>
      {/if}

      {#if isLinux || isWindows}
        <div>
          <div class="flex items-center justify-between mb-1.5">
            <div class="text-[11px] uppercase tracking-wider text-zinc-500">{isWindows ? 'Run in elevated PowerShell' : 'Run as root on the host'}</div>
            <button type="button" onclick={copy} class="text-[11px] px-2 py-0.5 rounded bg-zinc-800 hover:bg-zinc-700 {copyFailed ? 'text-rose-300' : 'text-zinc-200'}">{copied ? 'copied' : copyFailed ? 'copy failed — select manually' : 'copy'}</button>
          </div>
          <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-200">{command}</pre>
          <p class="mt-1.5 text-[11px] text-amber-300/80">Capabilities are set to exactly the boxes ticked above — anything unticked is removed if currently granted. {#if isLinux}Docker, GPU and network grants can't be detected from here; tick them if this agent already uses them.{/if}</p>
          <p class="mt-1 text-[11px] text-zinc-500">Pass <span class="font-mono">SM_REINSTALL=1</span> for a full fresh install instead.</p>
        </div>
      {/if}
    </div>

    <footer class="px-5 py-3 border-t border-zinc-800 flex items-center justify-end gap-2">
      <button
        type="button"
        onclick={onclose}
        class="text-xs px-3 py-1.5 rounded-md text-zinc-300 hover:text-zinc-100 hover:bg-zinc-800/60"
      >
        Close
      </button>
    </footer>
  </div>
</div>
