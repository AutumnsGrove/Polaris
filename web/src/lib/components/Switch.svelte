<script lang="ts">
	// The one on/off switch every settings surface uses. Svelte scopes CSS
	// per component, which is what used to make each new toggle copy the
	// styling into its own file — keep it here instead. A real checkbox
	// underneath, so keyboard and screen-reader behavior come free; `label`
	// is its accessible name.
	let {
		checked,
		label,
		disabled = false,
		onchange
	}: {
		checked: boolean;
		label: string;
		disabled?: boolean;
		onchange: (checked: boolean) => void;
	} = $props();
</script>

<label class="switch" class:disabled>
	<input
		type="checkbox"
		{checked}
		{disabled}
		aria-label={label}
		onchange={(e) => onchange(e.currentTarget.checked)}
	/>
	<span class="slider"></span>
</label>

<style>
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
		border-radius: var(--radius-full);
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
		transition:
			transform 0.15s ease,
			background 0.15s ease;
	}

	.switch input:checked + .slider {
		background: color-mix(in srgb, var(--color-accent) 30%, transparent);
		border-color: var(--color-accent);
	}

	.switch input:checked + .slider::before {
		transform: translateX(16px);
		background: var(--color-accent);
	}

	.switch input:focus-visible + .slider {
		outline: 2px solid var(--color-accent);
		outline-offset: 2px;
	}

	.switch.disabled {
		opacity: 0.45;
	}

	.switch.disabled .slider {
		cursor: default;
	}
</style>
