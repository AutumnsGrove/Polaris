<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { fly } from 'svelte/transition';
	import { quintOut } from 'svelte/easing';
	import { appState } from '$lib/state.svelte';
	import { fieldsState, fieldColorVar } from '$lib/fields.svelte';
	import EditTextModal from '$lib/components/EditTextModal.svelte';
	import FieldChips from '$lib/components/FieldChips.svelte';
	import { PanelLeft, Plus, Star, FolderOpen } from '@lucide/svelte';
	import type { Field } from '$lib/types';

	onMount(() => {
		void fieldsState.load();
	});

	let creating = $state(false);

	// A field is born with just a name — everything else (instructions,
	// defaults, files) is edited in its own detail view, so create drops
	// straight into it rather than making the hub a second, cramped editor.
	async function create(name: string) {
		creating = false;
		const res = await fieldsState.create({ name });
		if (!res.ok) {
			appState.showToast(res.error);
			return;
		}
		// Straight to Instructions: a brand-new field has nothing to open yet,
		// so the fill-it-in flow leads (the detail view defaults to Conversations).
		void goto(`/fields/${res.data.id}?tab=instructions`);
	}

	// A tap on the star must not also open the card it sits on.
	async function togglePin(e: MouseEvent, field: Field) {
		e.stopPropagation();
		const res = await fieldsState.update(field.id, { favorite: !field.favorite });
		if (!res.ok) appState.showToast(res.error);
	}

	function updatedLabel(iso: string): string {
		const d = new Date(iso);
		if (Number.isNaN(d.getTime())) return '';
		return `Updated ${d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })}`;
	}
</script>

<svelte:head>
	<title>Fields — Polaris</title>
</svelte:head>

<header class="header">
	<div class="header-left">
		{#if !appState.sidebarOpen}
			<button class="icon-btn" onclick={() => appState.toggleSidebar()} title="Open sidebar">
				<PanelLeft size={18} />
			</button>
		{/if}
		<h1 class="page-title"><span class="wordmark">Fields</span></h1>
	</div>
	<div class="header-right">
		<button class="btn btn-accent" onclick={() => (creating = true)}>
			<Plus size={16} />
			New <span class="wordmark">Field</span>
		</button>
	</div>
</header>

<div class="content">
	{#if fieldsState.error && !fieldsState.loaded}
		<p class="empty">Couldn't load your <span class="wordmark">Fields</span>. Check the connection and try again.</p>
	{:else if fieldsState.loaded && fieldsState.fields.length === 0}
		<p class="empty">
			No <span class="wordmark">Fields</span> yet. A <span class="wordmark">Field</span> groups conversations that share the same instructions and reference
			files — try "Budget rebuild" or "Trip planning".
		</p>
	{/if}

	<div class="grid">
		{#each fieldsState.fields as field, i (field.id)}
			<div
				class="card"
				style:--tint={fieldColorVar(field.color)}
				class:tinted={!!fieldColorVar(field.color)}
				onclick={() => goto(`/fields/${field.id}`)}
				onkeydown={(e) => e.key === 'Enter' && goto(`/fields/${field.id}`)}
				role="button"
				tabindex="0"
				in:fly={{ y: 8, duration: 220, delay: Math.min(i, 10) * 22, easing: quintOut }}
			>
				<div class="card-top">
					<FolderOpen size={15} class="card-icon" />
					<h2 class="card-name">{field.name}</h2>
					<button
						class="pin"
						class:pinned={field.favorite}
						onclick={(e) => togglePin(e, field)}
						title={field.favorite ? 'Unpin from sidebar' : 'Pin to sidebar'}
						aria-label={field.favorite ? 'Unpin from sidebar' : 'Pin to sidebar'}
						aria-pressed={field.favorite}
					>
						<Star size={15} fill={field.favorite ? 'currentColor' : 'none'} />
					</button>
				</div>
				{#if field.description}
					<p class="card-desc">{field.description}</p>
				{/if}
				<div class="card-bottom">
					<FieldChips {field} />
					<span class="card-meta">
						{field.thread_count}
						{field.thread_count === 1 ? 'thread' : 'threads'} · {updatedLabel(field.updated_at)}
					</span>
				</div>
			</div>
		{/each}
	</div>
</div>

{#if creating}
	<EditTextModal
		heading="New Field"
		placeholder="Field name"
		maxLength={100}
		onSave={create}
		onCancel={() => (creating = false)}
	/>
{/if}

<style>
	.header {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-md);
		padding: max(var(--space-lg), env(safe-area-inset-top)) var(--space-lg) var(--space-lg);
		box-shadow: var(--shadow-well);
	}

	.header-left {
		display: flex;
		align-items: center;
		gap: var(--space-md);
		min-width: 0;
	}

	.header-right {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.page-title {
		margin: 0;
		font-size: 20px;
	}

	/* Reserved brand-face treatment (see app.css's --font-wordmark) — this
	   page's title, "New Field" button, and empty-state copy render the
	   literal word "Field(s)" as a name, same as "Pulsar" on its own page. */
	.wordmark {
		font-family: var(--font-wordmark);
		font-weight: 400;
		letter-spacing: 0.02em;
	}

	.content {
		flex: 1;
		overflow-y: auto;
		padding: var(--space-lg);
	}

	.empty {
		max-width: 46ch;
		margin: var(--space-2xl) auto;
		text-align: center;
		font-size: 13.5px;
		line-height: 1.6;
		color: var(--color-text-dim);
	}

	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(260px, 1fr));
		gap: var(--space-md);
		max-width: 960px;
	}

	.card {
		position: relative;
		display: flex;
		flex-direction: column;
		gap: var(--space-sm);
		min-height: 108px;
		padding: var(--space-lg);
		border-radius: var(--radius-lg);
		background: var(--color-surface);
		box-shadow: var(--shadow-sm), var(--shadow-glass-edge);
		cursor: pointer;
		transition:
			background-color 0.15s var(--ease-out-expo),
			box-shadow 0.15s var(--ease-out-expo);
	}

	.card:hover {
		background: var(--color-surface-2);
		box-shadow: var(--shadow-md), var(--shadow-glass-edge);
	}

	/* A color-tagged card wears its tint as a soft wash from the top edge
	   instead of a side stripe — same restraint the rest of the app's
	   surfaces use (no colored borders). */
	.card.tinted {
		background: linear-gradient(
			to bottom,
			color-mix(in oklch, var(--tint) 14%, var(--color-surface)),
			var(--color-surface) 70%
		);
	}

	.card-top {
		display: flex;
		align-items: center;
		gap: var(--space-sm);
	}

	.card-top :global(.card-icon) {
		flex-shrink: 0;
		color: var(--tint, var(--color-text-dim));
	}

	.card-name {
		flex: 1;
		min-width: 0;
		margin: 0;
		font-size: 15px;
		font-weight: 600;
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.pin {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		flex-shrink: 0;
		width: 28px;
		height: 28px;
		border: none;
		border-radius: var(--radius-full);
		background: transparent;
		color: var(--color-text-dim);
		cursor: pointer;
		transition:
			background-color 0.15s var(--ease-out-expo),
			color 0.15s var(--ease-out-expo);
	}

	.pin:hover {
		background: var(--color-surface-3);
	}

	.pin.pinned {
		color: var(--color-accent);
	}

	.card-desc {
		margin: 0;
		font-size: 13px;
		line-height: 1.5;
		color: var(--color-text-dim);
		display: -webkit-box;
		-webkit-line-clamp: 2;
		line-clamp: 2;
		-webkit-box-orient: vertical;
		overflow: hidden;
	}

	.card-bottom {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: var(--space-sm);
		margin-top: auto;
	}

	.card-meta {
		margin-left: auto;
		font-size: 11.5px;
		color: var(--color-text-dim);
		white-space: nowrap;
	}
</style>
