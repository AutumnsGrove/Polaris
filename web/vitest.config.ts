import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vitest/config';

// Deliberately separate from vite.config.ts: that one wires up the
// sveltekit() plugin (adapter-static, $app/* aliases, the dev proxy to
// the Go backend) — none of which unit tests need, and pulling it in
// would mean tests implicitly depend on SvelteKit's routing/build
// machinery. Just the Svelte compiler (for the .svelte.ts files under
// test, which use runes) plus a DOM for anything touching
// document/localStorage.
export default defineConfig({
	plugins: [svelte({ compilerOptions: { runes: true } })],
	test: {
		environment: 'happy-dom',
		include: ['src/**/*.test.ts']
	}
});
