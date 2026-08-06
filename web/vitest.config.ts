import { fileURLToPath } from 'node:url';
import { svelte } from '@sveltejs/vite-plugin-svelte';
import { defineConfig } from 'vitest/config';

// Deliberately separate from vite.config.ts: that one wires up the full
// sveltekit() plugin (adapter-static, the dev proxy to the Go backend,
// route-manifest-aware $app/* resolution) — none of which unit tests need,
// and pulling it in would mean tests implicitly depend on SvelteKit's
// routing/build machinery. Just the Svelte compiler (for the .svelte.ts
// files under test, which use runes) plus a DOM for anything touching
// document/localStorage.
//
// $lib and $app/state are aliased here as plain path resolution ONLY —
// not the sveltekit() plugin's version of them — so route files that
// import via those specifiers (every real route does) can be mounted at
// all in a component test. $app/state's target is never actually used:
// every test that mounts a route importing it supplies its own
// vi.mock('$app/state', ...); this alias exists purely so Vite's static
// import analysis has something to resolve before that mock takes over.
export default defineConfig({
	plugins: [svelte({ compilerOptions: { runes: true } })],
	resolve: {
		alias: {
			$lib: fileURLToPath(new URL('./src/lib', import.meta.url)),
			'$app/state': fileURLToPath(new URL('./src/testSupport/appStateAliasStub.ts', import.meta.url))
		},
		// Without this, Node's package resolution picks Svelte's "svelte"
		// export condition (server-side rendering to a string) even though
		// happy-dom below gives us a real DOM to mount into — component
		// tests that call mount()/render() fail with "mount(...) is not
		// available on the server". "browser" is Svelte's own documented
		// fix for this exact vitest pitfall: https://svelte.dev/docs/svelte/testing
		conditions: ['browser']
	},
	test: {
		environment: 'happy-dom',
		include: ['src/**/*.test.ts']
	}
});
