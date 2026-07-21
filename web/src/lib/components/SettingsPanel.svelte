<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { X, Moon, Sun, RefreshCw } from '@lucide/svelte';

	type UpdateState = 'idle' | 'updating' | 'restarting' | 'error';
	let updateState = $state<UpdateState>('idle');
	let updateLog = $state('');

	function close() {
		appState.settings.open = false;
	}

	async function handlePushUpdate() {
		updateState = 'updating';
		updateLog = '';
		try {
			const result = await appState.settings.pushUpdate();
			updateLog = result.log ?? '';
			if (!result.success) {
				updateState = 'error';
				return;
			}
			if (!result.restarting) {
				// Not running under systemd/launchd — rebuilt, but nothing
				// will restart it automatically.
				updateState = 'idle';
				return;
			}
			updateState = 'restarting';
			await waitForServerAndReload();
		} catch (err) {
			updateLog = String(err);
			updateState = 'error';
		}
	}

	// The build (go build on the potato's ARM CPU can take a while) and
	// restart happen out from under this request, so poll until the
	// server answers again, then hard-reload — a normal client-side nav
	// would keep running the *old* JS bundle even after the backend and
	// its embedded frontend assets have updated.
	async function waitForServerAndReload() {
		const deadline = Date.now() + 120_000;
		while (Date.now() < deadline) {
			await new Promise((r) => setTimeout(r, 1500));
			try {
				const res = await fetch('/api/models', { cache: 'no-store' });
				if (res.ok) {
					window.location.reload();
					return;
				}
			} catch {
				// still down — keep polling
			}
		}
		updateState = 'error';
		updateLog += '\n\nServer did not come back within 2 minutes — check it manually.';
	}
</script>

<div class="backdrop" role="presentation">
	<button class="backdrop-close" onclick={close} aria-label="Close settings"></button>
	<div class="panel" role="dialog" aria-modal="true" aria-label="Settings">
		<div class="panel-header">
			<h2>Settings</h2>
			<button class="icon-btn" onclick={close} title="Close"><X size={18} /></button>
		</div>

		<section>
			<h3>Appearance</h3>
			<div class="row">
				<span>Theme</span>
				<div class="theme-toggle">
					<button
						class:active={appState.settings.theme === 'dark'}
						onclick={() => appState.settings.setTheme('dark')}
					>
						<Moon size={14} /> Dark
					</button>
					<button
						class:active={appState.settings.theme === 'light'}
						onclick={() => appState.settings.setTheme('light')}
					>
						<Sun size={14} /> Light
					</button>
				</div>
			</div>

			<div class="row">
				<span>Show prices</span>
				<label class="switch">
					<input
						type="checkbox"
						checked={appState.settings.showPrices}
						onchange={(e) => appState.settings.setShowPrices(e.currentTarget.checked)}
					/>
					<span class="slider"></span>
				</label>
			</div>
		</section>

		<section>
			<h3>Model</h3>
			<div class="row">
				<span>Default model</span>
				<select
					value={appState.settings.defaultModel}
					onchange={(e) => appState.settings.setDefaultModel(e.currentTarget.value, () => appState.loadModels())}
				>
					{#each appState.models as model (model.id)}
						<option value={model.id}>{model.name}</option>
					{/each}
				</select>
			</div>
			<p class="hint">
				Applies to new threads. You can still switch models per-thread from the chat header.
			</p>
		</section>

		<section>
			<h3>Updates</h3>
			{#if appState.version}
				<div class="row version-row">
					<span>Version</span>
					<code class="version">{appState.version}</code>
				</div>
			{/if}
			<button class="btn update-btn" onclick={handlePushUpdate} disabled={updateState !== 'idle' && updateState !== 'error'}>
				<RefreshCw size={14} class={updateState === 'updating' || updateState === 'restarting' ? 'spin' : ''} />
				{#if updateState === 'updating'}
					Pulling & building…
				{:else if updateState === 'restarting'}
					Restarting…
				{:else}
					Push update now
				{/if}
			</button>
			<p class="hint">Pulls the latest code, rebuilds the frontend and binary, then restarts.</p>
			{#if updateLog}
				<pre class="log">{updateLog}</pre>
			{/if}
		</section>
	</div>
</div>

<style>
	.backdrop {
		position: fixed;
		inset: 0;
		z-index: 100;
		display: flex;
		align-items: center;
		justify-content: center;
		padding: 16px;
	}

	.backdrop-close {
		position: absolute;
		inset: 0;
		border: none;
		padding: 0;
		/* Darker backdrop for stronger separation between modal and
		   the ground behind it — this is the one modal in the app and
		   can afford real contrast when it opens. Blur stays purposeful,
		   not decorative (banned as content-surface treatment). */
		background: rgba(0, 0, 0, 0.62);
		backdrop-filter: blur(8px);
		-webkit-backdrop-filter: blur(8px);
		cursor: default;
	}

	.panel {
		position: relative;
		width: 100%;
		max-width: 440px;
		max-height: 85vh;
		overflow-y: auto;
		/* Slightly lifted surface color so the modal reads as elevated
		   above the sidebar/main. */
		background: var(--color-surface-2);
		border: 1px solid var(--color-border-strong);
		border-radius: var(--radius-lg);
		/* Substantially deeper shadow than any inline element — this is
		   the only floating panel in the app, so it can afford to feel
		   heavy on entry. */
		box-shadow:
			0 32px 80px -20px rgba(0, 0, 0, 0.6),
			0 12px 32px -12px rgba(0, 0, 0, 0.45),
			0 0 0 1px rgba(0, 0, 0, 0.2);
		padding: 24px 24px 20px;
	}

	:root[data-theme='light'] .panel {
		box-shadow:
			0 32px 80px -20px rgba(50, 40, 28, 0.28),
			0 12px 32px -12px rgba(50, 40, 28, 0.18);
	}

	.panel-header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		margin-bottom: 18px;
	}

	.panel-header h2 {
		margin: 0;
		font-family: var(--font-serif);
		font-size: 22px;
		font-weight: 700;
		letter-spacing: -0.005em;
	}

	section {
		margin-bottom: 18px;
		padding-bottom: 18px;
		border-bottom: 1px solid var(--color-border);
	}

	section:last-child {
		border-bottom: none;
		margin-bottom: 0;
		padding-bottom: 0;
	}

	/* Small-caps section labels: heavier weight + wider tracking so the
	   contrast against 400-weight body copy underneath reads as confident
	   rather than timid. Text color pushed up a notch so labels aren't
	   ghost-dim. */
	section h3 {
		margin: 0 0 12px 0;
		font-size: 11px;
		font-weight: 700;
		text-transform: uppercase;
		letter-spacing: 0.12em;
		color: var(--color-text);
	}

	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 12px;
		margin-bottom: 8px;
		font-size: 14px;
	}

	.hint {
		font-size: 12px;
		color: var(--color-text-dim);
		margin: 6px 0 0 0;
	}

	.theme-toggle {
		display: flex;
		border: 1px solid var(--color-border);
		border-radius: var(--radius-md);
		overflow: hidden;
	}

	.theme-toggle button {
		display: flex;
		align-items: center;
		gap: 6px;
		border: none;
		background: transparent;
		padding: 6px 12px;
		font-size: 13px;
		color: var(--color-text-dim);
		transition: background-color 0.15s var(--ease-out-expo), color 0.15s var(--ease-out-expo);
	}

	.theme-toggle button:hover {
		color: var(--color-text);
	}

	.theme-toggle button.active {
		background: var(--color-accent-soft);
		color: var(--color-text);
		font-weight: 600;
	}

	select {
		border: 1px solid var(--color-border);
		background: var(--color-surface-2);
		border-radius: var(--radius-md);
		padding: 6px 10px;
		font-size: 13px;
	}

	.switch {
		position: relative;
		display: inline-block;
		width: 36px;
		height: 20px;
		flex-shrink: 0;
	}

	.switch input {
		opacity: 0;
		width: 0;
		height: 0;
	}

	.slider {
		position: absolute;
		inset: 0;
		background: var(--color-surface-2);
		border: 1px solid var(--color-border);
		border-radius: 999px;
		cursor: pointer;
		transition: background 0.15s ease;
	}

	.slider::before {
		content: '';
		position: absolute;
		width: 14px;
		height: 14px;
		left: 2px;
		top: 2px;
		background: var(--color-text-dim);
		border-radius: 50%;
		transition: transform 0.15s ease, background 0.15s ease;
	}

	.switch input:checked + .slider {
		background: color-mix(in srgb, var(--color-accent) 30%, transparent);
		border-color: var(--color-accent);
	}

	.switch input:checked + .slider::before {
		transform: translateX(16px);
		background: var(--color-accent);
	}

	.version-row {
		margin-bottom: 12px;
	}

	.version {
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
		font-size: 12px;
		color: var(--color-text-dim);
		background: var(--color-bg);
		padding: 4px 8px;
		border-radius: var(--radius-sm);
		border: 1px solid var(--color-border);
	}

	.update-btn {
		width: 100%;
	}

	.log {
		margin-top: 12px;
		padding: 10px 12px;
		background: var(--color-bg);
		border: 1px solid var(--color-border);
		border-radius: var(--radius-sm);
		font-size: 11px;
		line-height: 1.5;
		color: var(--color-text-dim);
		white-space: pre-wrap;
		word-break: break-word;
		max-height: 180px;
		overflow-y: auto;
		font-family: ui-monospace, 'SF Mono', Menlo, Consolas, monospace;
	}

	:global(.spin) {
		animation: spin 1s linear infinite;
	}

	@keyframes spin {
		to {
			transform: rotate(360deg);
		}
	}
</style>
