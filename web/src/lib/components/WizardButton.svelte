<script lang="ts">
	import { Sparkles } from '@lucide/svelte';

	// The small "Help me write this" launcher that sits in a prompt-writing
	// field's label row and opens WizardOverlay. One shared component
	// because every surface that offers the wizard (Pulsar routine form,
	// Pulsar Daily config, Field instructions) needs the identical button —
	// Svelte scopes component CSS per file, so without this each surface
	// carried its own copy of the same rules, and a restyle meant finding
	// all of them.
	let { onclick }: { onclick: () => void } = $props();
</script>

<button type="button" class="wizard-btn" {onclick}>
	<Sparkles size={12} />
	Help me write this
</button>

<style>
	.wizard-btn {
		display: flex;
		align-items: center;
		gap: var(--space-xs);
		margin-bottom: var(--space-xs);
		padding: 2px var(--space-sm);
		border: none;
		background: transparent;
		border-radius: var(--radius-full);
		font-size: 11.5px;
		font-weight: 600;
		color: var(--color-accent);
	}

	.wizard-btn:hover {
		background: var(--color-accent-soft);
	}
</style>
