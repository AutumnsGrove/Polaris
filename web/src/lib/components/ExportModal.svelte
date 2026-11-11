<script lang="ts">
	import { appState } from '$lib/state.svelte';
	import { X, Download, Copy, FileText, Braces } from '@lucide/svelte';
	import { swipeToDismiss } from '$lib/actions/swipeToDismiss';
	import Switch from './Switch.svelte';

	// Export one thread as Markdown or versioned JSON
	// (docs/plans/thread-export.md). All the work is server-side: this modal
	// just collects the options, asks the export endpoint for a dry-run
	// estimate, and then either downloads the file or copies its text.
	let { threadId, onClose }: { threadId: string; onClose: () => void } = $props();

	let format = $state<'md' | 'json'>('md');
	let scope = $state<'full' | 'handoff'>('full');
	let sources = $state(true);
	let attachments = $state(false);
	let meta = $state(false);
	let trail = $state(false);
	let thinking = $state(false);
	let privateMode = $state(true);
	let privatePersonal = $state(false);
	let delivery = $state<'download' | 'copy'>('download');

	let busy = $state(false);
	let error = $state('');
	let estimate = $state<number | null>(null);
	let availability = $state<{
		turns: number;
		turns_with_trail?: number;
		turns_with_thinking?: number;
		handoff_available?: boolean;
	} | null>(null);

	let handoffAvailable = $derived(availability?.handoff_available === true);

	// Query string for the chosen options — read by the dry-run effect below
	// (so every toggle re-estimates) and by the actual download/copy request.
	function query(): string {
		const q = new URLSearchParams();
		q.set('format', format);
		q.set('scope', scope);
		q.set('sources', sources ? '1' : '0');
		q.set('attachments', attachments ? '1' : '0');
		q.set('meta', meta ? '1' : '0');
		q.set('trail', trail ? '1' : '0');
		q.set('thinking', thinking ? '1' : '0');
		q.set('private', privateMode ? '1' : '0');
		q.set('private_personal', privatePersonal ? '1' : '0');
		return q.toString();
	}

	// Debounced so dragging through several toggles fires one request, not
	// one per tap. The effect depends on every option via query().
	let timer: ReturnType<typeof setTimeout> | undefined;
	$effect(() => {
		const q = query();
		clearTimeout(timer);
		timer = setTimeout(() => void loadEstimate(q), 250);
		return () => clearTimeout(timer);
	});

	async function loadEstimate(q: string) {
		try {
			const res = await fetch(`/api/threads/${threadId}/export?${q}&dry_run=1`);
			if (!res.ok) return;
			const data = await res.json();
			estimate = data.tokens;
			availability = data.availability;
			// Handoff was picked but this thread has no stored summary: fall
			// back to full rather than request a scope that would export
			// nothing. The option is disabled below regardless.
			if (!handoffAvailable && scope === 'handoff') scope = 'full';
		} catch {
			// Leave the previous estimate; the action buttons will surface a
			// real error if the export itself can't be fetched.
		}
	}

	function availabilityNote(): string {
		if (!availability) return '';
		const parts: string[] = [];
		if (thinking) {
			parts.push(`Thinking: ${availability.turns_with_thinking ?? 0} of ${availability.turns} turns`);
		}
		if (trail) {
			parts.push(`Trail: ${availability.turns_with_trail ?? 0} of ${availability.turns} turns`);
		}
		return parts.join(' · ');
	}

	function filenameFrom(header: string | null, fallback: string): string {
		const match = header?.match(/filename="([^"]+)"/);
		return match ? match[1] : fallback;
	}

	async function download() {
		busy = true;
		error = '';
		try {
			const res = await fetch(`/api/threads/${threadId}/export?${query()}`);
			if (!res.ok) {
				error = res.status === 404 ? 'That thread no longer exists.' : 'Export failed — please try again.';
				return;
			}
			const blob = await res.blob();
			const name = filenameFrom(res.headers.get('Content-Disposition'), `polaris-thread.${format}`);
			const url = URL.createObjectURL(blob);
			const a = document.createElement('a');
			a.href = url;
			a.download = name;
			a.click();
			URL.revokeObjectURL(url);
			onClose();
		} catch {
			error = 'Export failed — check your connection and try again.';
		} finally {
			busy = false;
		}
	}

	async function copy() {
		busy = true;
		error = '';
		try {
			const res = await fetch(`/api/threads/${threadId}/export?${query()}`);
			if (!res.ok) {
				error = res.status === 404 ? 'That thread no longer exists.' : 'Export failed — please try again.';
				return;
			}
			await navigator.clipboard.writeText(await res.text());
			appState.showToast('Copied export');
			onClose();
		} catch {
			error = 'Copy failed — clipboard access was blocked.';
		} finally {
			busy = false;
		}
	}

	function onKeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') onClose();
	}
</script>

<svelte:window onkeydown={onKeydown} />

<div class="modal-backdrop" role="presentation">
	<button class="modal-backdrop-close" onclick={onClose} aria-label="Close"></button>
	<div class="modal-panel" role="dialog" aria-modal="true" aria-label="Export thread">
		<div class="sheet-handle" use:swipeToDismiss={onClose} aria-hidden="true"></div>
		<div class="modal-panel-header">
			<h2>Export thread</h2>
			<button class="icon-btn" onclick={onClose} title="Close"><X size={18} /></button>
		</div>

		<div class="export-body">
			<section>
				<h3>Format</h3>
				<div class="seg">
					<button class="seg-btn" class:active={format === 'md'} onclick={() => (format = 'md')}>
						<FileText size={15} /> Markdown
					</button>
					<button class="seg-btn" class:active={format === 'json'} onclick={() => (format = 'json')}>
						<Braces size={15} /> JSON
					</button>
				</div>
			</section>

			<section>
				<h3>Scope</h3>
				<div class="seg">
					<button class="seg-btn" class:active={scope === 'full'} onclick={() => (scope = 'full')}>
						Full conversation
					</button>
					<button
						class="seg-btn"
						class:active={scope === 'handoff'}
						disabled={!handoffAvailable}
						onclick={() => (scope = 'handoff')}
					>
						Handoff
					</button>
				</div>
				{#if !handoffAvailable}
					<p class="hint">Handoff needs a stored summary, and this thread doesn't have one.</p>
				{/if}
			</section>

			<section>
				<h3>Include</h3>
				<div class="row">
					<span>Sources and citations</span>
					<Switch checked={sources} label="Sources and citations" onchange={(v) => (sources = v)} />
				</div>
				<div class="row">
					<span>Attachments <span class="dim">(names and types only)</span></span>
					<Switch checked={attachments} label="Attachments" onchange={(v) => (attachments = v)} />
				</div>
				<div class="row">
					<span>Cost and model metadata</span>
					<Switch checked={meta} label="Cost and model metadata" onchange={(v) => (meta = v)} />
				</div>
				<div class="row">
					<span>Research trail <span class="dim">(searches, pages, tool calls)</span></span>
					<Switch checked={trail} label="Research trail" onchange={(v) => (trail = v)} />
				</div>
				<div class="row">
					<span>Thinking traces</span>
					<Switch checked={thinking} label="Thinking traces" onchange={(v) => (thinking = v)} />
				</div>
			</section>

			<section>
				<h3>Privacy</h3>
				<div class="row">
					<span>Leave out memories and custom instructions</span>
					<Switch checked={privateMode} label="Leave out memories and custom instructions" onchange={(v) => (privateMode = v)} />
				</div>
				<div class="row">
					<span>Also leave out chat search, star reads and save-to-field</span>
					<Switch checked={privatePersonal} label="Also leave out personalization tools" onchange={(v) => (privatePersonal = v)} />
				</div>
				<p class="hint">
					Privacy drops those tool calls from the trail and never reads the system prompt. It can't stop an
					answer, a thinking trace or a summary from quoting a memory, or scrub text you typed.
				</p>
			</section>

			<section>
				<h3>Delivery</h3>
				<div class="seg">
					<button class="seg-btn" class:active={delivery === 'download'} onclick={() => (delivery = 'download')}>
						<Download size={15} /> Download
					</button>
					<button class="seg-btn" class:active={delivery === 'copy'} onclick={() => (delivery = 'copy')}>
						<Copy size={15} /> Copy
					</button>
				</div>
			</section>

			{#if estimate !== null || availabilityNote()}
				<div class="estimate">
					{#if estimate !== null}<span>≈ {estimate.toLocaleString()} tokens</span>{/if}
					{#if availabilityNote()}<span class="dim">{availabilityNote()}</span>{/if}
				</div>
			{/if}

			{#if error}<p class="error">{error}</p>{/if}
		</div>

		<div class="modal-actions">
			<button class="btn" onclick={onClose}>Cancel</button>
			<button
				class="btn btn-accent"
				disabled={busy}
				onclick={delivery === 'copy' ? copy : download}
			>
				{#if delivery === 'copy'}<Copy size={15} />{:else}<Download size={15} />{/if}
				{delivery === 'copy' ? 'Copy' : 'Download'}
			</button>
		</div>
	</div>
</div>

<style>
	.export-body {
		display: flex;
		flex-direction: column;
		gap: var(--space-xl);
		overflow-y: auto;
	}

	section h3 {
		margin: 0 0 var(--space-sm);
		font-size: 12px;
		font-weight: 600;
		letter-spacing: 0.04em;
		text-transform: uppercase;
		color: var(--color-text-dim);
	}

	.seg {
		display: flex;
		gap: var(--space-xs);
		background: var(--color-surface-2);
		box-shadow: var(--shadow-well);
		border-radius: var(--radius-md);
		padding: var(--space-xs);
	}

	.seg-btn {
		flex: 1;
		display: flex;
		align-items: center;
		justify-content: center;
		gap: var(--space-xs);
		border: none;
		background: transparent;
		border-radius: var(--radius-sm);
		padding: var(--space-sm);
		font: inherit;
		font-size: 13px;
		color: var(--color-text-dim);
		cursor: pointer;
	}

	.seg-btn.active {
		background: var(--color-surface-3);
		color: var(--color-text);
		box-shadow: var(--shadow-sm);
	}

	.seg-btn:disabled {
		opacity: 0.5;
		cursor: default;
	}

	.row {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-lg);
		padding: var(--space-sm) 0;
		font-size: 14px;
	}

	.dim {
		color: var(--color-text-dim);
	}

	.hint {
		margin: var(--space-xs) 0 0;
		font-size: 12px;
		line-height: 1.5;
		color: var(--color-text-dim);
	}

	.estimate {
		display: flex;
		flex-direction: column;
		gap: var(--space-xs);
		font-size: 13px;
		color: var(--color-text);
	}

	.error {
		margin: 0;
		font-size: 13px;
		color: var(--color-danger);
	}

	.modal-actions {
		display: flex;
		justify-content: flex-end;
		gap: var(--space-sm);
		margin-top: var(--space-xl);
	}
</style>
