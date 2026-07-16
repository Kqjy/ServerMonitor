import adapter from '@sveltejs/adapter-static';
import { vitePreprocess } from '@sveltejs/vite-plugin-svelte';

export default {
  preprocess: vitePreprocess(),
  kit: {
    version: {
      pollInterval: 60_000
    },
    adapter: adapter({
      pages: '../internal/server/web/dist',
      assets: '../internal/server/web/dist',
      fallback: 'index.html',
      precompress: false,
      strict: false
    }),
    alias: {
      $lib: 'src/lib'
    }
  }
};
