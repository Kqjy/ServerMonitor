export const prerender = false;
export const ssr = false;
export const trailingSlash = 'ignore';

import { auth } from '$lib/auth.svelte';

export async function load() {
  await auth.refresh();
  return {};
}
