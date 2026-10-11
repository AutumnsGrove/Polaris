<script lang="ts">
	import { Sparkles } from '@lucide/svelte';

	// One small component, reused at two scopes rather than two separate
	// indicator concepts — see docs/plans/pulsar-routines.md's "Amber
	// indicator semantics": the sidebar's Orbit icon (global count) and
	// each routine row inside /pulsar (count scoped to that one routine)
	// both render this, just with a different `count`. Renders nothing
	// for count <= 0 so a caller never needs its own {#if} guard.
	//
	// `alert` swaps the plain dot for a filled starlight chip: at least one
	// of the unread pulses flagged itself as big news (alert_me). Still the
	// same amber, just louder in shape rather than in color.
	//
	// --color-accent is PRODUCT.md's "warm starlight gold" — already
	// amber, not a new token.
	let { count, alert = false }: { count: number; alert?: boolean } = $props();
</script>

{#if count > 0}
	{#if alert}
		<span class="pulsar-alert" title="Big news" aria-label="Big news">
			<Sparkles size={11} strokeWidth={2.5} color="var(--color-bg)" />
			{#if count > 1}
				<span class="alert-count">{count > 99 ? '99+' : count}</span>
			{/if}
		</span>
	{:else}
		<span class="pulsar-badge" class:with-count={count > 1}>
			{#if count > 1}
				{count > 99 ? '99+' : count}
			{/if}
		</span>
	{/if}
{/if}

<style>
	/* 1 unread: dot only. */
	.pulsar-badge {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 8px;
		height: 8px;
		border-radius: 50%;
		background: var(--color-accent);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 22%, transparent);
		flex-shrink: 0;
	}

	/* >1 unread: dot grows into an inbox-style count pill instead of a
	   second, competing shape. */
	.pulsar-badge.with-count {
		width: auto;
		height: 16px;
		min-width: 16px;
		padding: 0 5px;
		border-radius: 999px;
		box-shadow: none;
		font-size: 10px;
		font-weight: 700;
		line-height: 1;
		color: var(--color-bg);
	}

	/* Big news: a filled gold chip with a sparkle — one shape, not a
	   second color, so it stays calm next to the plain dots. */
	.pulsar-alert {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		gap: 3px;
		min-width: 18px;
		height: 18px;
		padding: 0 5px;
		border-radius: 999px;
		background: var(--color-accent);
		color: var(--color-bg);
		box-shadow: 0 0 0 3px color-mix(in srgb, var(--color-accent) 22%, transparent);
		flex-shrink: 0;
	}

	.alert-count {
		font-size: 10px;
		font-weight: 700;
		line-height: 1;
	}
</style>
