<script lang="ts">
	// One small component, reused at two scopes rather than two separate
	// indicator concepts — see docs/plans/pulsar-routines.md's "Amber
	// indicator semantics": the sidebar's Orbit icon (global count) and
	// each routine row inside /pulsar (count scoped to that one routine)
	// both render this, just with a different `count`. Renders nothing
	// for count <= 0 so a caller never needs its own {#if} guard.
	//
	// --color-accent is PRODUCT.md's "warm starlight gold" — already
	// amber, not a new token.
	let { count }: { count: number } = $props();
</script>

{#if count > 0}
	<span class="pulsar-badge" class:with-count={count > 1}>
		{#if count > 1}
			{count > 99 ? '99+' : count}
		{/if}
	</span>
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
</style>
