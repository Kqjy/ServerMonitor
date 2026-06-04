<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import { api, type AgentPlatform, type ServerInfo, type Host } from '$lib/api';
  import { goto } from '$app/navigation';

  type Step = 'name' | 'install';
  type PlatformID = 'linux-amd64' | 'linux-arm64' | 'windows-amd64' | 'darwin-amd64' | 'darwin-arm64' | 'manual';

  let step = $state<Step>('name');
  let hostname = $state('');
  let interval = $state(10);
  let busy = $state(false);
  let error = $state<string | null>(null);

  const intervalOptions = [5, 10, 30, 60];

  let token = $state('');
  let hostId = $state<number | null>(null);

  let platforms = $state<AgentPlatform[]>([]);
  let serverInfo = $state<ServerInfo | null>(null);
  let active = $state<PlatformID>('linux-amd64');

  let waitingHost = $state<Host | null>(null);
  let connected = $state(false);
  let poll: ReturnType<typeof setInterval> | null = null;
  let copied = $state<string | null>(null);

  let enablePortOwners = $state(false);
  let enableDocker = $state(false);
  let enableSmart = $state(false);
  let enableSmartNvme = $state(false);
  let enableGpu = $state(false);
  let enableNetwork = $state(false);
  let adminService = $state(false);

  async function loadMeta() {
    try {
      const [ps, info] = await Promise.all([api.agentPlatforms(), api.serverInfo()]);
      platforms = ps;
      serverInfo = info;
      const guessed = guessPlatform();
      if (ps.some((p) => p.id === guessed)) active = guessed as PlatformID;
    } catch (e) {
      error = (e as Error).message;
    }
  }

  function guessPlatform(): string {
    const ua = navigator.userAgent.toLowerCase();
    if (ua.includes('windows')) return 'windows-amd64';
    if (ua.includes('mac')) return ua.includes('arm') ? 'darwin-arm64' : 'darwin-amd64';
    return 'linux-amd64';
  }

  async function register() {
    if (!hostname.trim()) {
      error = 'Hostname required';
      return;
    }
    busy = true;
    error = null;
    try {
      const res = await api.registerHost(hostname.trim(), interval);
      token = res.token;
      hostId = res.host_id;
      step = 'install';
      startPolling();
    } catch (e) {
      error = (e as Error).message;
    } finally {
      busy = false;
    }
  }

  function startPolling() {
    if (poll) clearInterval(poll);
    poll = setInterval(async () => {
      if (hostId === null) return;
      try {
        const h = await api.host(hostId);
        waitingHost = h;
        if (h.last_seen) connected = true;
      } catch {}
    }, 2000);
  }

  function copy(text: string, key: string) {
    void navigator.clipboard?.writeText(text);
    copied = key;
    setTimeout(() => (copied = null), 1500);
  }

  function goToHost() {
    if (hostId !== null) void goto(`/hosts/${hostId}`);
  }

  const baseUrl = $derived(serverInfo?.url ?? (typeof window !== 'undefined' ? window.location.origin : ''));

  const shellOneLiner = $derived.by(() => {
    const vars = [`SM_INTERVAL=${interval}`];
    if (enablePortOwners) vars.push('SM_ENABLE_PORT_OWNERS=1');
    if (enableDocker)  vars.push('SM_ENABLE_DOCKER=1');
    if (enableSmart)   vars.push('SM_ENABLE_SMART=1');
    if (enableSmart && enableSmartNvme) vars.push('SM_ENABLE_SMART_NVME=1');
    if (enableGpu)     vars.push('SM_ENABLE_GPU=1');
    if (enableNetwork) vars.push('SM_ENABLE_NETWORK=1');
    const preserve = vars.map((v) => v.split('=')[0]).join(',');
    return `${vars.join(' ')} sudo --preserve-env=${preserve} bash -c "curl -fsSL ${baseUrl}/install.sh | bash"`;
  });
  const pwshOneLiner = $derived.by(() => {
    const parts = [`$env:SM_INTERVAL="${interval}"`];
    if (adminService) parts.push('$env:SM_ADMIN_SERVICE="1"');
    if (enableSmart)  parts.push('$env:SM_ENABLE_SMART="1"');
    return `${parts.join('; ')}; iex (iwr -useb ${baseUrl}/install.ps1).Content`;
  });

  const tomlSnippet = $derived(
    `server_url = "${baseUrl}"
token      = "${token || '<TOKEN>'}"
interval_s = ${interval}`
  );

  const manualLinux = $derived(
    `sudo install -o root -g root -m 0700 -d /etc/servermonitor /var/lib/servermonitor
sudo curl -fsSL --config - -o /usr/local/bin/sm-agent "${baseUrl}/api/v1/agent/binary?platform=${active === 'manual' ? 'linux-amd64' : active}" <<CURLCFG
header = "X-Agent-Token: ${token || '<TOKEN>'}"
CURLCFG
sudo chown root:root /usr/local/bin/sm-agent
sudo chmod 0755 /usr/local/bin/sm-agent
sudo install -o root -g root -m 0600 /dev/stdin /etc/servermonitor/agent.toml <<EOF
${tomlSnippet}
EOF
sudo /usr/local/bin/sm-agent --config /etc/servermonitor/agent.toml`
  );

  const manualWindows = $derived(
    `function Lock-Path([string]$p) {
  $isDir = (Get-Item -Force -LiteralPath $p).PSIsContainer
  $acl = Get-Acl $p
  $acl.SetAccessRuleProtection($true, $false)
  foreach ($r in @($acl.Access)) { [void]$acl.RemoveAccessRule($r) }
  $sys   = New-Object Security.Principal.SecurityIdentifier 'S-1-5-18'
  $adm   = New-Object Security.Principal.SecurityIdentifier 'S-1-5-32-544'
  $full  = [Security.AccessControl.FileSystemRights]::FullControl
  $inh   = if ($isDir) { [Security.AccessControl.InheritanceFlags] 'ContainerInherit,ObjectInherit' } else { [Security.AccessControl.InheritanceFlags]::None }
  $allow = [Security.AccessControl.AccessControlType]::Allow
  $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($sys, $full, $inh, 'None', $allow)))
  $acl.AddAccessRule((New-Object Security.AccessControl.FileSystemAccessRule($adm, $full, $inh, 'None', $allow)))
  $acl.SetOwner($adm)
  Set-Acl -Path $p -AclObject $acl
}
$installDir = "$env:ProgramFiles\\ServerMonitor"
$configDir  = "$env:ProgramData\\ServerMonitor"
$exe        = "$installDir\\sm-agent.exe"
$cfg        = "$configDir\\agent.toml"
foreach ($d in @($installDir, $configDir)) {
  if (-not (Test-Path $d)) { New-Item -ItemType Directory -Path $d | Out-Null }
  Lock-Path $d
}
Invoke-WebRequest -UseBasicParsing -Headers @{ 'X-Agent-Token' = '${token || '<TOKEN>'}' } -OutFile $exe "${baseUrl}/api/v1/agent/binary?platform=windows-amd64"
Lock-Path $exe
@"
${tomlSnippet}
"@ | Set-Content -Encoding UTF8 $cfg
Lock-Path $cfg
& $exe --config $cfg`
  );

  function platformLabel(id: string) {
    return platforms.find((p) => p.id === id)?.label ?? id;
  }

  onMount(loadMeta);
  onDestroy(() => {
    if (poll) clearInterval(poll);
  });
</script>

<div class="max-w-3xl mx-auto px-4 sm:px-6 py-6 sm:py-8">
  <div class="mb-2">
    <a href="/" class="text-xs text-zinc-500 hover:text-zinc-300">← All hosts</a>
  </div>
  <h1 class="text-xl sm:text-2xl font-semibold tracking-tight">Add a host</h1>
  <p class="text-xs sm:text-sm text-zinc-500 mt-1">Generate an agent token, then run one command on the target server.</p>

  <ol class="mt-6 flex flex-wrap items-center gap-3 text-xs">
    <li class="flex items-center gap-2">
      <span class="h-5 w-5 rounded-full grid place-items-center {step === 'name' ? 'bg-emerald-500/20 border border-emerald-500/40 text-emerald-200' : 'bg-zinc-800 text-zinc-300 border border-zinc-700'} numeric">1</span>
      <span class={step === 'name' ? 'text-zinc-100' : 'text-zinc-400'}>Name</span>
    </li>
    <span class="h-px w-8 bg-zinc-800"></span>
    <li class="flex items-center gap-2">
      <span class="h-5 w-5 rounded-full grid place-items-center {step === 'install' ? 'bg-emerald-500/20 border border-emerald-500/40 text-emerald-200' : 'bg-zinc-800 text-zinc-500 border border-zinc-700'} numeric">2</span>
      <span class={step === 'install' ? 'text-zinc-100' : 'text-zinc-500'}>Install</span>
    </li>
    <span class="h-px w-8 bg-zinc-800"></span>
    <li class="flex items-center gap-2">
      <span class="h-5 w-5 rounded-full grid place-items-center {connected ? 'bg-emerald-500/20 border border-emerald-500/40 text-emerald-200' : 'bg-zinc-800 text-zinc-500 border border-zinc-700'} numeric">3</span>
      <span class={connected ? 'text-zinc-100' : 'text-zinc-500'}>Connected</span>
    </li>
  </ol>

  {#if step === 'name'}
    <section class="mt-6 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5">
      <label for="hostname" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Hostname</label>
      <input
        id="hostname"
        type="text"
        bind:value={hostname}
        autocomplete="off"
        spellcheck="false"
        placeholder="web-01"
        onkeydown={(e) => { if (e.key === 'Enter') register(); }}
        class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono" />
      <p class="mt-1.5 text-[11px] text-zinc-600">Use a unique name. The hostname appears everywhere this host shows up.</p>

      <div class="mt-5">
        <div class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Sample interval</div>
        <div class="flex items-center gap-1 text-xs">
          {#each intervalOptions as s (s)}
            <button
              type="button"
              onclick={() => (interval = s)}
              class="px-3 py-1.5 rounded-md transition-colors numeric {interval === s ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">
              {s}s
            </button>
          {/each}
        </div>
        <p class="mt-1.5 text-[11px] text-zinc-600">How often the agent collects and uploads. Default 10s is fine for most hosts.</p>
      </div>

      {#if error}
        <div class="mt-3 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{error}</div>
      {/if}

      <div class="mt-4 flex justify-end gap-2">
        <a href="/" class="text-sm px-3 py-2 rounded-md text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/40">Cancel</a>
        <button
          type="button"
          disabled={busy || !hostname.trim()}
          onclick={register}
          class="text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50 disabled:cursor-not-allowed font-medium">
          {busy ? 'Generating…' : 'Generate token'}
        </button>
      </div>
    </section>
  {:else}
    <section class="mt-6 space-y-5">
      <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5">
        <div class="flex items-baseline justify-between gap-3 flex-wrap">
          <div class="min-w-0">
            <div class="text-xs uppercase tracking-wider text-zinc-500">Token for <span class="font-mono text-zinc-300">{hostname}</span></div>
            <p class="mt-1 text-[11px] text-amber-300/80">Saved. You won't see this token again — copy it now or use the install command below.</p>
          </div>
          <button type="button" onclick={() => copy(token, 'token')} class="text-xs px-2.5 py-1 rounded-md bg-zinc-800 hover:bg-zinc-700 text-zinc-200 shrink-0">
            {copied === 'token' ? 'copied' : 'Copy token'}
          </button>
        </div>
        <code class="block mt-3 text-xs font-mono text-zinc-100 break-all bg-zinc-950/60 rounded px-3 py-2 border border-zinc-800">{token}</code>
      </div>

      <div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
        <header class="px-4 sm:px-5 py-3 border-b border-zinc-800 flex flex-wrap items-center justify-between gap-3">
          <h2 class="text-sm font-medium text-zinc-100">Install on the target server</h2>
          <div class="flex items-center gap-1 text-[11px] overflow-x-auto no-scrollbar -mx-1 px-1 max-w-full">
            {#each platforms as p (p.id)}
              <button
                type="button"
                onclick={() => (active = p.id as PlatformID)}
                class="px-2 py-1 rounded-md transition-colors shrink-0 {active === p.id ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">
                {p.label}
              </button>
            {/each}
            <button
              type="button"
              onclick={() => (active = 'manual')}
              class="px-2 py-1 rounded-md transition-colors shrink-0 {active === 'manual' ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">
              Manual
            </button>
          </div>
        </header>

        <div class="p-4 sm:p-5 space-y-4 text-sm">
          {#if active === 'manual'}
            <p class="text-xs text-zinc-500">Download the binary, drop in <code class="font-mono text-zinc-300">agent.toml</code>, run.</p>

            <div>
              <div class="text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5">agent.toml</div>
              <div class="relative">
                <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-300">{tomlSnippet}</pre>
                <button type="button" onclick={() => copy(tomlSnippet, 'toml')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'toml' ? 'copied' : 'copy'}</button>
              </div>
            </div>

            <div class="grid grid-cols-1 md:grid-cols-2 gap-3">
              {#each platforms as p (p.id)}
                <a
                  href={`/api/v1/agent/binary?platform=${p.id}`}
                  download
                  class="flex items-center justify-between px-3 py-2 rounded-md border border-zinc-800 hover:border-zinc-700 hover:bg-zinc-800/30 text-xs">
                  <span class="text-zinc-200">{p.label}</span>
                  <span class="text-zinc-500 numeric">{(p.size / 1024 / 1024).toFixed(1)} MB ↓</span>
                </a>
              {/each}
            </div>
          {:else if active.startsWith('windows')}
            <div class="space-y-2 pb-3 border-b border-zinc-800/60">
              <div class="text-[11px] uppercase tracking-wider text-zinc-500">Optional capabilities</div>
              <p class="text-[11px] text-zinc-600 -mt-1">Off by default. Default install runs as a virtual <span class="font-mono">NT SERVICE\sm-agent</span> account.</p>
              <label class="flex items-start gap-2 text-xs text-zinc-300 cursor-pointer select-none">
                <input type="checkbox" bind:checked={adminService} class="mt-0.5 accent-emerald-500" />
                <span><span class="text-zinc-100">Admin service</span> <span class="text-zinc-500">— runs as <span class="font-mono">LocalSystem</span> (needed for SMART and full process visibility)</span></span>
              </label>
              <label class="flex items-start gap-2 text-xs text-zinc-300 cursor-pointer select-none">
                <input type="checkbox" bind:checked={enableSmart} class="mt-0.5 accent-emerald-500" />
                <span><span class="text-zinc-100">Disk SMART</span> <span class="text-zinc-500">— auto-installs <span class="font-mono">smartmontools</span> via <span class="font-mono">winget</span> (also requires Admin service)</span></span>
              </label>
            </div>
            <p class="text-xs text-zinc-500">Open PowerShell as Administrator on <span class="font-mono text-zinc-300">{platformLabel(active)}</span> and paste — the installer will prompt for the token shown above:</p>
            <div class="relative">
              <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-200">{pwshOneLiner}</pre>
              <button type="button" onclick={() => copy(pwshOneLiner, 'ps1')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'ps1' ? 'copied' : 'copy'}</button>
            </div>
            <details class="text-xs text-zinc-500">
              <summary class="cursor-pointer hover:text-zinc-300">Manual smoke-test (no service)</summary>
              <p class="mt-2 text-[11px] text-amber-300/70">Runs the agent once in the foreground for verification. Does not install the hardened Windows service — use the one-liner above for production.</p>
              <div class="relative mt-2">
                <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-300">{manualWindows}</pre>
                <button type="button" onclick={() => copy(manualWindows, 'manualWin')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'manualWin' ? 'copied' : 'copy'}</button>
              </div>
            </details>
          {:else}
            {#if active.startsWith('linux')}
              <div class="space-y-2 pb-3 border-b border-zinc-800/60">
                <div class="text-[11px] uppercase tracking-wider text-zinc-500">Optional capabilities</div>
                <p class="text-[11px] text-zinc-600 -mt-1">Off by default. Each grants extra privilege to <span class="font-mono">sm-agent</span> for that collector.</p>
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
            {/if}
            <p class="text-xs text-zinc-500">SSH to <span class="font-mono text-zinc-300">{platformLabel(active)}</span> and run — the installer will prompt for the token shown above:</p>
            <div class="relative">
              <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-200">{shellOneLiner}</pre>
              <button type="button" onclick={() => copy(shellOneLiner, 'sh')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'sh' ? 'copied' : 'copy'}</button>
            </div>
            <details class="text-xs text-zinc-500">
              <summary class="cursor-pointer hover:text-zinc-300">Manual smoke-test (no service)</summary>
              <p class="mt-2 text-[11px] text-amber-300/70">Runs the agent once in the foreground as root for verification. Does not create the hardened systemd unit, dedicated user, or capability set — use the one-liner above for production.</p>
              <div class="relative mt-2">
                <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-300">{manualLinux}</pre>
                <button type="button" onclick={() => copy(manualLinux, 'manualLin')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'manualLin' ? 'copied' : 'copy'}</button>
              </div>
            </details>
          {/if}
        </div>
      </div>

      <div class="rounded-xl border {connected ? 'border-emerald-900/40 bg-emerald-950/20' : 'border-zinc-800 bg-zinc-900/40'} p-4 sm:p-5">
        {#if connected}
          <div class="flex flex-wrap items-center gap-3">
            <span class="h-2 w-2 rounded-full bg-emerald-400 animate-pulse shrink-0"></span>
            <div class="flex-1 min-w-0">
              <div class="text-sm font-medium text-emerald-100">Connected — first metrics arrived</div>
              <div class="text-xs text-zinc-400 mt-0.5 numeric">
                {waitingHost?.os || '—'}{waitingHost?.arch ? ` · ${waitingHost.arch}` : ''}{waitingHost?.agent_version ? ` · agent v${waitingHost.agent_version}` : ''}
              </div>
            </div>
            <button type="button" onclick={goToHost} class="text-sm px-3 py-1.5 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 shrink-0">Open host →</button>
          </div>
        {:else}
          <div class="flex items-center gap-3">
            <span class="h-2 w-2 rounded-full bg-amber-400 animate-pulse shrink-0"></span>
            <div class="flex-1 min-w-0">
              <div class="text-sm text-zinc-200 break-words">Waiting for first metrics from <span class="font-mono">{hostname}</span>…</div>
              <div class="text-xs text-zinc-500 mt-0.5">This page will turn green once the agent reports in.</div>
            </div>
          </div>
        {/if}
      </div>
    </section>
  {/if}
</div>
