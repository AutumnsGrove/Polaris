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
			})
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
