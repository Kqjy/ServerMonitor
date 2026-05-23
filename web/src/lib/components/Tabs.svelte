<script lang="ts">
  let {
    tabs,
    value = $bindable()
  }: {
    tabs: { value: string; label: string }[];
    value: string;
  } = $props();
</script>

<div class="border-b border-zinc-800 -mx-4 px-4 sm:mx-0 sm:px-0 overflow-x-auto no-scrollbar">
  <nav class="flex items-center gap-x-1 text-sm whitespace-nowrap">
    {#each tabs as t (t.value)}
      <button
        type="button"
        onclick={() => (value = t.value)}
        class="px-3 py-2 -mb-px border-b-2 transition-colors shrink-0 {value === t.value ? 'border-emerald-400 text-zinc-100' : 'border-transparent text-zinc-500 hover:text-zinc-300'}">
        {t.label}
      </button>
    {/each}
  </nav>
</div>
