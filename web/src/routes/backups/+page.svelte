<script lang="ts">
  import { onMount, onDestroy } from 'svelte';
  import {
    api,
    type BackupTargetsResp,
    type BackupTarget,
    type BackupCredential,
    type Host
  } from '$lib/api';
  import { bytes, timeAgo } from '$lib/format';
  import ConfirmDialog from '$lib/components/ConfirmDialog.svelte';

  type View = 'list' | 'new';
  type SnippetTab = 'linux' | 'windows' | 'restic' | 'existing';

  let view = $state<View>('list');
  let data = $state<BackupTargetsResp | null>(null);
  let hosts = $state<Host[]>([]);
  let baseUrl = $state('');
  let loading = $state(true);
  let error = $state<string | null>(null);
  let measuring = $state<number | null>(null);

  let newName = $state('');
  let newHostId = $state<number | null>(null);
  let newQuotaGiB = $state<number | null>(null);
  let creating = $state(false);
  let createError = $state<string | null>(null);
  let credential = $state<BackupCredential | null>(null);
  let snippetTab = $state<SnippetTab>('linux');
  let copied = $state<string | null>(null);

  let rotated = $state<BackupCredential | null>(null);
  let toRevoke = $state<BackupTarget | null>(null);
  let toDelete = $state<BackupTarget | null>(null);

  let firstBlobSeen = $state(false);
  let poll: ReturnType<typeof setInterval> | null = null;

  async function load() {
    loading = true;
    error = null;
    try {
      const [resp, info] = await Promise.all([api.backupTargets(), api.serverInfo()]);
      data = resp;
      baseUrl = (info.url || (typeof window !== 'undefined' ? window.location.origin : '')).replace(/\/+$/, '');
    } catch (e) {
      error = (e as Error).message;
    } finally {
      loading = false;
    }
  }

  async function loadHosts() {
    try {
      hosts = await api.hosts();
    } catch {}
  }

  function openNew() {
    newName = '';
    newHostId = null;
    newQuotaGiB = null;
    credential = null;
    createError = null;
    firstBlobSeen = false;
    snippetTab = 'linux';
    view = 'new';
    void loadHosts();
  }

  function backToList() {
    stopPoll();
    view = 'list';
    void load();
  }

  const nameValid = $derived(/^[a-zA-Z0-9._-]+$/.test(newName.trim()) && newName.trim() !== '.' && newName.trim() !== '..');

  async function create() {
    if (!nameValid) {
      createError = 'Name must use only letters, digits, dot, dash or underscore.';
      return;
    }
    creating = true;
    createError = null;
    try {
      const body: { name: string; host_id?: number; quota_bytes?: number } = { name: newName.trim() };
      if (newHostId != null) body.host_id = newHostId;
      if (newQuotaGiB != null && newQuotaGiB > 0) body.quota_bytes = Math.round(newQuotaGiB * 1024 ** 3);
      credential = await api.backupTargetCreate(body);
      startPoll();
    } catch (e) {
      createError = (e as Error).message;
    } finally {
      creating = false;
    }
  }

  function startPoll() {
    stopPoll();
    poll = setInterval(async () => {
      if (!credential) return;
      try {
        const resp = await api.backupTargets();
        data = resp;
        const t = resp.targets.find((x) => x.id === credential!.id);
        if (t && t.used_bytes > 0) firstBlobSeen = true;
      } catch {}
    }, 2500);
  }

  function stopPoll() {
    if (poll) clearInterval(poll);
    poll = null;
  }

  async function measure(id: number) {
    measuring = id;
    try {
      const updated = await api.backupTargetMeasure(id);
      if (data) data.targets = data.targets.map((t) => (t.id === id ? updated : t));
    } catch (e) {
      error = (e as Error).message;
    } finally {
      measuring = null;
    }
  }

  async function rotate(id: number) {
    try {
      rotated = await api.backupTargetRotate(id);
      await load();
    } catch (e) {
      error = (e as Error).message;
    }
  }

  async function doRevoke() {
    if (!toRevoke) return;
    await api.backupTargetRevoke(toRevoke.id);
    toRevoke = null;
    await load();
  }

  async function doDelete() {
    if (!toDelete) return;
    await api.backupTargetDelete(toDelete.id);
    toDelete = null;
    await load();
  }

  function copy(text: string, key: string) {
    void navigator.clipboard?.writeText(text).catch(() => {});
    copied = key;
    setTimeout(() => (copied = null), 1500);
  }

  function sharePct(used: number, quota?: number): number {
    if (!quota || quota <= 0) return 0;
    return Math.min(100, Math.round((used / quota) * 100));
  }

  function barTone(used: number, quota?: number): string {
    const pct = sharePct(used, quota);
    if (pct >= 95) return 'bg-rose-500/70';
    if (pct >= 80) return 'bg-amber-500/70';
    return 'bg-sky-500/70';
  }

  const repoUrl = $derived(credential ? `rest:${baseUrl}/backup/${credential.name}` : '');

  const linuxSnippet = $derived.by(() => {
    if (!credential) return '';
    const vars = [
      'SM_ADMIN_TOKEN=<ADMIN_TOKEN>',
      'SM_ENABLE_BACKUP=1',
      `SM_BACKUP_REPOS="${repoUrl}"`,
      `SM_BACKUP_REPO_NAMES="${credential.name}"`,
      `SM_BACKUP_REST_USERNAME="${credential.name}"`,
      `SM_BACKUP_REST_PASSWORD="${credential.password}"`,
      'SM_BACKUP_PRUNE_MODE=external'
    ];
    const preserve = vars.map((v) => v.split('=')[0]).join(',');
    return `${vars.join(' \\\n  ')} \\\n  sudo --preserve-env=${preserve} bash -c "curl -fsSL ${baseUrl}/install.sh | bash"`;
  });

  const windowsSnippet = $derived.by(() => {
    if (!credential) return '';
    return [
      '$env:SM_ADMIN_TOKEN="<ADMIN_TOKEN>"',
      '$env:SM_ENABLE_BACKUP="1"',
      `$env:SM_BACKUP_REPOS="${repoUrl}"`,
      `$env:SM_BACKUP_REPO_NAMES="${credential.name}"`,
      `$env:SM_BACKUP_REST_USERNAME="${credential.name}"`,
      `$env:SM_BACKUP_REST_PASSWORD="${credential.password}"`,
      '$env:SM_BACKUP_PRUNE_MODE="external"',
      `iex (iwr -useb ${baseUrl}/install.ps1).Content`
    ].join('\n');
  });

  const existingToml = $derived.by(() => {
    if (!credential) return '';
    return `[[repo]]
name = "${credential.name}"
url = "${repoUrl}"
password_file = "/etc/servermonitor-backup/backup.key"
env_file = "/etc/servermonitor-backup/repo-credentials.env"`;
  });

  const existingEnv = $derived.by(() => {
    if (!credential) return '';
    return `RESTIC_REST_USERNAME=${credential.name}
RESTIC_REST_PASSWORD=${credential.password}`;
  });

  const resticSnippet = $derived.by(() => {
    if (!credential) return '';
    return `export RESTIC_REST_USERNAME=${credential.name}
export RESTIC_REST_PASSWORD=${credential.password}
restic -r ${repoUrl} init
restic -r ${repoUrl} backup /etc`;
  });

  const activeSnippet = $derived(
    snippetTab === 'linux' ? linuxSnippet : snippetTab === 'windows' ? windowsSnippet : resticSnippet
  );

  onMount(load);
  onDestroy(stopPoll);
</script>

<div class="max-w-7xl mx-auto px-4 sm:px-6 py-6 sm:py-8">
  <div class="flex items-start justify-between gap-3 flex-wrap">
    <div>
      <h1 class="text-xl sm:text-2xl font-semibold tracking-tight">Backup targets</h1>
      <p class="text-xs sm:text-sm text-zinc-500 mt-1">
        Host encrypted restic backups for your fleet. This server is the append-only endpoint other hosts back up to.
      </p>
    </div>
    {#if view === 'list' && data?.configured}
      <button
        type="button"
        onclick={openNew}
        class="text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 font-medium shrink-0">
        New backup target
      </button>
    {:else if view === 'new'}
      <button type="button" onclick={backToList} class="text-sm px-3 py-2 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 shrink-0">
        ← All targets
      </button>
    {/if}
  </div>

  {#if error}
    <div class="mt-4 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{error}</div>
  {/if}

  {#if loading && !data}
    <div class="mt-6 h-40 rounded-xl shimmer"></div>
  {:else if data && !data.configured}
    <section class="mt-6 rounded-xl border border-zinc-800 bg-zinc-900/40 p-6 text-center">
      <div class="text-sm text-zinc-300">The backup server is not enabled.</div>
      <p class="mt-2 text-xs text-zinc-500 max-w-lg mx-auto">
        Set <code class="font-mono text-zinc-300">BACKUP_DIR</code> to store backups on this server's disk, or
        <code class="font-mono text-zinc-300">BACKUP_S3_BUCKET</code> (with <code class="font-mono text-zinc-300">BACKUP_S3_ENDPOINT</code> for
        self-hosted S3) to store them in an object-storage bucket, then restart the server.
      </p>
    </section>
  {:else if view === 'list' && data}
    {#if data.tls.mode === 'insecure'}
      <div class="mt-4 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2.5 text-xs text-rose-300">
        No TLS. The backup endpoint authenticates with HTTP Basic, so credentials would travel in plaintext. Set
        <span class="font-mono">TLS_CERT_FILE</span>/<span class="font-mono">TLS_KEY_FILE</span>, put it behind a TLS proxy
        (<span class="font-mono">TRUST_PROXY_TLS=1</span>), or set <span class="font-mono">BACKUP_ACME_DOMAIN</span> for automatic Let's Encrypt.
      </div>
    {:else if data.tls.mode === 'acme'}
      <div class="mt-4 rounded-md border border-emerald-900/50 bg-emerald-950/25 px-3 py-2.5 text-xs text-emerald-300">
        Automatic TLS (Let's Encrypt) for <span class="font-mono">{data.tls.domain}</span> — a certificate is obtained on the first HTTPS connection.
      </div>
    {:else}
      <div class="mt-4 rounded-md border border-emerald-900/40 bg-emerald-950/20 px-3 py-2.5 text-xs text-emerald-300/90">
        TLS active ({data.tls.mode === 'proxy' ? 'terminated by a trusted reverse proxy' : 'native certificate'}). Backup traffic is encrypted.
      </div>
    {/if}

    <section class="mt-4 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
      <div class="grid grid-cols-1 sm:grid-cols-3 divide-y sm:divide-y-0 sm:divide-x divide-zinc-800">
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Backend</div>
          <div class="text-lg font-semibold text-zinc-100 mt-1 break-all">{data.storage?.kind === 's3' ? 'Object storage' : 'Local disk'}</div>
          <div class="text-[11px] text-zinc-500 mt-0.5 font-mono break-all">{data.storage?.location}</div>
        </div>
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Stored</div>
          <div class="text-2xl font-semibold text-zinc-100 numeric mt-1">{bytes(data.targets.reduce((s, t) => s + t.used_bytes, 0))}</div>
          <div class="text-[11px] text-zinc-500 mt-0.5">across all targets</div>
        </div>
        <div class="px-4 sm:px-5 py-4">
          <div class="text-[11px] uppercase tracking-wider text-zinc-500">Targets</div>
          <div class="text-2xl font-semibold text-zinc-100 numeric mt-1">{data.targets.filter((t) => !t.revoked_at).length}</div>
          <div class="text-[11px] text-zinc-500 mt-0.5">{data.targets.filter((t) => t.revoked_at).length} revoked</div>
        </div>
      </div>
    </section>

    <section class="mt-4 rounded-xl border border-zinc-800 bg-zinc-900/40 overflow-hidden">
      {#if data.targets.length === 0}
        <div class="px-4 py-10 text-center text-sm text-zinc-500">
          No backup targets yet. Create one, then point a host at it.
        </div>
      {:else}
        <div class="overflow-x-auto">
          <table class="w-full text-sm">
            <thead>
              <tr class="text-left text-[11px] uppercase tracking-wider text-zinc-500 border-b border-zinc-800">
                <th class="px-4 py-2.5 font-medium">Target</th>
                <th class="px-4 py-2.5 font-medium">Linked host</th>
                <th class="px-4 py-2.5 font-medium">Used</th>
                <th class="px-4 py-2.5 font-medium">Measured</th>
                <th class="px-4 py-2.5 font-medium">Created</th>
                <th class="px-4 py-2.5 font-medium text-right">Actions</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-zinc-800/70">
              {#each data.targets as t (t.id)}
                <tr class="hover:bg-zinc-900/60">
                  <td class="px-4 py-3 align-top">
                    <div class="flex items-center gap-2">
                      <span class="h-1.5 w-1.5 rounded-full {t.revoked_at ? 'bg-rose-400' : 'bg-emerald-400'}"></span>
                      <span class="font-mono text-zinc-200">{t.name}</span>
                    </div>
                    {#if t.revoked_at}
                      <span class="ml-3.5 text-[10px] uppercase tracking-wider text-rose-300">revoked</span>
                    {/if}
                  </td>
                  <td class="px-4 py-3 align-top text-zinc-400 text-xs">{t.hostname || '—'}</td>
                  <td class="px-4 py-3 align-top">
                    <div class="numeric text-zinc-200 text-xs">{bytes(t.used_bytes)}{#if t.quota_bytes}<span class="text-zinc-500"> / {bytes(t.quota_bytes)}</span>{/if}</div>
                    {#if t.quota_bytes}
                      <div class="mt-1.5 h-1.5 w-28 rounded-full bg-zinc-800 overflow-hidden">
                        <div class="h-full rounded-full {barTone(t.used_bytes, t.quota_bytes)}" style="width: {sharePct(t.used_bytes, t.quota_bytes)}%"></div>
                      </div>
                    {:else}
                      <div class="text-[10px] text-zinc-600 mt-0.5">no quota</div>
                    {/if}
                  </td>
                  <td class="px-4 py-3 align-top text-zinc-400 text-xs numeric whitespace-nowrap">{t.usage_measured_at ? timeAgo(t.usage_measured_at) : 'never'}</td>
                  <td class="px-4 py-3 align-top text-zinc-400 text-xs numeric whitespace-nowrap">{timeAgo(t.created_at)}</td>
                  <td class="px-4 py-3 align-top">
                    <div class="flex items-center justify-end gap-1.5">
                      <button
                        type="button"
                        onclick={() => measure(t.id)}
                        disabled={measuring === t.id}
                        class="text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 disabled:opacity-50">
                        {measuring === t.id ? 'Measuring…' : 'Measure'}
                      </button>
                      <button
                        type="button"
                        onclick={() => rotate(t.id)}
                        class="text-[11px] px-2 py-1 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60">
                        Rotate
                      </button>
                      {#if t.used_bytes === 0}
                        <button
                          type="button"
                          onclick={() => (toDelete = t)}
                          title="This target holds no backups — safe to remove"
                          class="text-[11px] px-2 py-1 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">
                          Delete
                        </button>
                      {:else if !t.revoked_at}
                        <button
                          type="button"
                          onclick={() => (toRevoke = t)}
                          class="text-[11px] px-2 py-1 rounded-md border border-rose-500/40 text-rose-300 hover:bg-rose-500/10">
                          Revoke
                        </button>
                      {/if}
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}
    </section>

    <p class="mt-3 text-[11px] text-zinc-600">
      Revoking a credential stops new uploads but never deletes stored backups — this endpoint is append-only, including for admins.
      Remove blobs directly on the storage backend if you need to reclaim space. Empty targets that never received an upload can be deleted outright.
    </p>
  {:else if view === 'new'}
    {#if !credential}
      <section class="mt-6 rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5 max-w-2xl">
        <h2 class="text-sm font-medium text-zinc-100">New backup target</h2>
        <p class="text-xs text-zinc-500 mt-1">A private repository namespace plus a credential the target host uses to upload.</p>

        <div class="mt-4">
          <label for="bt-name" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Name</label>
          <input
            id="bt-name"
            type="text"
            bind:value={newName}
            autocomplete="off"
            spellcheck="false"
            placeholder="web-01-offsite"
            class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm font-mono" />
          <p class="mt-1.5 text-[11px] text-zinc-600">Letters, digits, dot, dash, underscore. Becomes the repo path and login name.</p>
        </div>

        <div class="mt-4">
          <label for="bt-host" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Linked host <span class="text-zinc-600 normal-case">(optional)</span></label>
          <select
            id="bt-host"
            bind:value={newHostId}
            class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm">
            <option value={null}>— none —</option>
            {#each hosts as h (h.id)}
              <option value={h.id}>{h.hostname}</option>
            {/each}
          </select>
          <p class="mt-1.5 text-[11px] text-zinc-600">Just for display — associates this target with a monitored host.</p>
        </div>

        <div class="mt-4">
          <label for="bt-quota" class="block text-xs uppercase tracking-wider text-zinc-500 mb-1.5">Quota <span class="text-zinc-600 normal-case">(GiB, optional)</span></label>
          <input
            id="bt-quota"
            type="number"
            min="0"
            step="1"
            bind:value={newQuotaGiB}
            placeholder="unlimited"
            class="w-full rounded-md bg-zinc-950 border border-zinc-800 focus:border-zinc-600 focus:outline-none px-3 py-2 text-sm numeric" />
          <p class="mt-1.5 text-[11px] text-zinc-600">Uploads are refused once the repo exceeds this. Leave blank for no limit.</p>
        </div>

        {#if createError}
          <div class="mt-3 rounded-md border border-rose-900/50 bg-rose-950/30 px-3 py-2 text-xs text-rose-300">{createError}</div>
        {/if}

        <div class="mt-4 flex justify-end gap-2">
          <button type="button" onclick={backToList} class="text-sm px-3 py-2 rounded-md text-zinc-400 hover:text-zinc-200 hover:bg-zinc-800/40">Cancel</button>
          <button
            type="button"
            disabled={creating || !nameValid}
            onclick={create}
            class="text-sm px-4 py-2 rounded-md bg-emerald-500/20 border border-emerald-500/40 text-emerald-200 hover:bg-emerald-500/30 disabled:opacity-50 disabled:cursor-not-allowed font-medium">
            {creating ? 'Creating…' : 'Create target'}
          </button>
        </div>
      </section>
    {:else}
      <section class="mt-6 space-y-5">
        <div class="rounded-xl border border-zinc-800 bg-zinc-900/40 p-4 sm:p-5">
          <div class="flex items-baseline justify-between gap-3 flex-wrap">
            <div class="min-w-0">
              <div class="text-xs uppercase tracking-wider text-zinc-500">Credential for <span class="font-mono text-zinc-300">{credential.name}</span></div>
              <p class="mt-1 text-[11px] text-amber-300/80">
                Shown once and stored only as a hash. It authenticates uploads only — it cannot decrypt backups (the repo password never leaves the host).
              </p>
            </div>
            <button type="button" onclick={() => credential && copy(credential.password, 'pw')} class="text-xs px-2.5 py-1 rounded-md bg-zinc-800 hover:bg-zinc-700 text-zinc-200 shrink-0">
              {copied === 'pw' ? 'copied' : 'Copy password'}
            </button>
          </div>
          <code class="block mt-3 text-xs font-mono text-zinc-100 break-all bg-zinc-950/60 rounded px-3 py-2 border border-zinc-800">{credential.password}</code>
          <div class="mt-3 flex items-center gap-2">
            <span class="text-[11px] uppercase tracking-wider text-zinc-500 shrink-0">Repo URL</span>
            <code class="flex-1 text-xs font-mono text-zinc-300 break-all bg-zinc-950/60 rounded px-3 py-1.5 border border-zinc-800">{repoUrl}</code>
            <button type="button" onclick={() => copy(repoUrl, 'url')} class="text-[11px] px-2 py-1 rounded-md bg-zinc-800 hover:bg-zinc-700 text-zinc-200 shrink-0">{copied === 'url' ? 'copied' : 'copy'}</button>
          </div>
        </div>

        <div class="rounded-xl border border-zinc-800 bg-zinc-900/40">
          <header class="px-4 sm:px-5 py-3 border-b border-zinc-800 flex flex-wrap items-center justify-between gap-3">
            <h2 class="text-sm font-medium text-zinc-100">Point a host at it</h2>
            <div class="flex items-center gap-1 text-[11px]">
              {#each [{ id: 'linux', label: 'Linux' }, { id: 'windows', label: 'Windows' }, { id: 'existing', label: 'Existing host' }, { id: 'restic', label: 'Verify with restic' }] as tab (tab.id)}
                <button
                  type="button"
                  onclick={() => (snippetTab = tab.id as SnippetTab)}
                  class="px-2 py-1 rounded-md transition-colors {snippetTab === tab.id ? 'bg-zinc-100/10 text-zinc-100' : 'text-zinc-500 hover:text-zinc-300 hover:bg-zinc-800/40'}">
                  {tab.label}
                </button>
              {/each}
            </div>
          </header>
          <div class="p-4 sm:p-5 space-y-3 text-sm">
            <p class="text-xs text-zinc-500">
              {#if snippetTab === 'linux'}
                Run on the target host. It provisions the backup timer and writes the credential to a root-owned file. Replace <span class="font-mono">&lt;ADMIN_TOKEN&gt;</span> with your server admin token.
              {:else if snippetTab === 'windows'}
                Run in an elevated PowerShell on the target host. Replace <span class="font-mono">&lt;ADMIN_TOKEN&gt;</span> with your server admin token.
              {:else if snippetTab === 'existing'}
                Already-monitored host with backups enabled? Add this repo to its backup config, then re-run the installer to apply.
              {:else}
                Verify connectivity by hand with the restic CLI ({'>='}0.17). Nothing is executed from this UI.
              {/if}
            </p>
            {#if snippetTab === 'existing'}
              <div>
                <div class="text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5">Add to <span class="font-mono normal-case">/etc/servermonitor-backup/backup.toml</span> <span class="text-zinc-600 normal-case">(root-owned)</span></div>
                <div class="relative">
                  <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-200 select-text">{existingToml}</pre>
                  <button type="button" onclick={() => copy(existingToml, 'etoml')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'etoml' ? 'copied' : 'copy'}</button>
                </div>
              </div>
              <div>
                <div class="text-[11px] uppercase tracking-wider text-zinc-500 mb-1.5">Write <span class="font-mono normal-case">/etc/servermonitor-backup/repo-credentials.env</span> <span class="text-zinc-600 normal-case">(root:sm-agent, mode 0440)</span></div>
                <div class="relative">
                  <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-200 select-text">{existingEnv}</pre>
                  <button type="button" onclick={() => copy(existingEnv, 'eenv')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'eenv' ? 'copied' : 'copy'}</button>
                </div>
              </div>
            {:else}
              <div class="relative">
                <pre class="text-xs font-mono bg-zinc-950 border border-zinc-800 rounded-md p-3 overflow-x-auto whitespace-pre text-zinc-200 select-text">{activeSnippet}</pre>
                <button type="button" onclick={() => copy(activeSnippet, 'snippet')} class="absolute top-2 right-2 text-[11px] px-2 py-0.5 rounded bg-zinc-800/80 hover:bg-zinc-700 text-zinc-300">{copied === 'snippet' ? 'copied' : 'copy'}</button>
              </div>
            {/if}
          </div>
        </div>

        <div class="rounded-xl border {firstBlobSeen ? 'border-emerald-900/40 bg-emerald-950/20' : 'border-zinc-800 bg-zinc-900/40'} p-4 sm:p-5">
          <div class="flex flex-wrap items-center gap-3">
            <span class="h-2 w-2 rounded-full {firstBlobSeen ? 'bg-emerald-400' : 'bg-amber-400'} animate-pulse shrink-0"></span>
            <div class="flex-1 min-w-0">
              {#if firstBlobSeen}
                <div class="text-sm font-medium text-emerald-100">Receiving backups — first data arrived</div>
                <div class="text-xs text-zinc-400 mt-0.5">This target is live. You can add another, or head back to the list.</div>
              {:else}
                <div class="text-sm text-zinc-200">Waiting for the first upload from <span class="font-mono">{credential.name}</span>…</div>
                <div class="text-xs text-zinc-500 mt-0.5">Turns green once the host runs its first backup to this endpoint.</div>
              {/if}
            </div>
            <button type="button" onclick={backToList} class="text-sm px-3 py-1.5 rounded-md border border-zinc-700 text-zinc-300 hover:bg-zinc-800/60 shrink-0">Done</button>
          </div>
        </div>
      </section>
    {/if}
  {/if}
</div>

<ConfirmDialog
  open={rotated !== null}
  title="New credential"
  confirmLabel="Done"
  cancelLabel="Close"
  onconfirm={() => { rotated = null; }}
  onclose={() => (rotated = null)}>
  {#snippet body()}
    <div class="space-y-2">
      <p class="text-xs text-amber-300/80">
        The old credential for <span class="font-mono">{rotated?.name}</span> no longer works. Update the host with this new password — shown once.
      </p>
      <code class="block text-xs font-mono text-zinc-100 break-all bg-zinc-950/60 rounded px-3 py-2 border border-zinc-800">{rotated?.password}</code>
      <button type="button" onclick={() => rotated && copy(rotated.password, 'rot')} class="text-[11px] px-2 py-1 rounded-md bg-zinc-800 hover:bg-zinc-700 text-zinc-200">{copied === 'rot' ? 'copied' : 'Copy password'}</button>
    </div>
  {/snippet}
</ConfirmDialog>

<ConfirmDialog
  open={toRevoke !== null}
  title="Revoke backup target"
  body={revokeBody}
  confirmLabel="Revoke"
  danger
  onconfirm={doRevoke}
  onclose={() => (toRevoke = null)} />

{#snippet revokeBody()}
  <p class="text-sm text-zinc-300">
    Revoke the credential for <span class="font-mono text-zinc-100">{toRevoke?.name}</span>? Its host can no longer upload.
    Stored backups are kept (append-only) and can still be restored from another credentialed machine.
  </p>
{/snippet}

<ConfirmDialog
  open={toDelete !== null}
  title="Delete backup target"
  body={deleteBody}
  confirmLabel="Delete"
  danger
  onconfirm={doDelete}
  onclose={() => (toDelete = null)} />

{#snippet deleteBody()}
  <p class="text-sm text-zinc-300">
    Permanently delete <span class="font-mono text-zinc-100">{toDelete?.name}</span>? It holds no backups, so nothing is
    lost — this just removes the credential and its entry.
  </p>
  <p class="mt-2 text-xs text-zinc-500">
    Only empty targets can be deleted. If an upload has landed since this list loaded, the delete is refused and you can
    revoke instead.
  </p>
{/snippet}
