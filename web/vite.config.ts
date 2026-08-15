import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';

export default defineConfig({
	plugins: [
		sveltekit({
			compilerOptions: {
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			// Static SPA build: plain HTML/CSS/JS, no Node server at runtime.
			// The Go backend embeds `build/` via go:embed and serves it directly —
			// fallback: 'index.html' makes this a client-routed single page app.
			adapter: adapter({
				pages: 'build',
				assets: 'build',
				fallback: 'index.html',
				precompress: false,
				strict: true
			}),
			// SvelteKit stamps a __sveltekit_<random> global into the client
			// bundle by default, regenerated randomly on every build — used for
			// SvelteKit's own built-in "new version, reload" detection, which
			// this app doesn't use at all (state.svelte.ts's checkVersion/
			// /api/version is a separate, purpose-built mechanism; see its own
			// doc comments). Left random, that stamp alone makes web/build/
			// non-reproducible between two otherwise-identical builds — every
			// chunk that transitively imports the module containing it gets a
			// new content hash too, cascading into most of the bundle. Pinned
			// to a fixed string so two builds from the same source are
			// byte-identical, which is what frontend-build-sync.yml's CI check
			// (and the Docker image's own from-source frontend build) both
			// assume.
			version: { name: 'polaris' }
		})
	],
	server: {
		// Local dev: `vite dev` proxies API + WebSocket calls to the Go
		// backend running on :8899, so the frontend gets hot reload while
		// still talking to the real agent loop.
		proxy: {
			'/api': 'http://localhost:8899',
			'/ws': {
				target: 'ws://localhost:8899',
				ws: true
			}
		}
	}
});
