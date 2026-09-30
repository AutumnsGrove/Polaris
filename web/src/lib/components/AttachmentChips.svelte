<script lang="ts">
	import { Paperclip } from '@lucide/svelte';
	import type { MessageAttachment } from '$lib/types';

	// The files attached to a user message. A chip is a real download once the
	// server has round-tripped a reload and assigned it a workspace_file_id;
	// a just-sent message shows the plain cosmetic chip until then.
	let { attachments, threadId }: { attachments: MessageAttachment[]; threadId: string | null } = $props();
</script>

<div class="attachment-chips">
	{#each attachments as attachment, i (attachment.filename + i)}
		{#if attachment.workspace_file_id && threadId}
			<!-- workspace_file_id is only known once the server
				round-trips a reload (see buildTurnsFromMessages) — a
				just-sent message shows the plain cosmetic chip below
				until then, same as before this unification. Uploads now
				persist for the thread's life instead of being deleted
				after one read, so this is a real download, not just a
				label. -->
			<a
				class="attachment-chip attachment-chip-link"
				href={`/api/workspace/${threadId}/${attachment.workspace_file_id}`}
				download={attachment.filename}
			>
				<Paperclip size={12} />
				<span>{attachment.filename}</span>
			</a>
		{:else}
			<div class="attachment-chip">
				<Paperclip size={12} />
				<span>{attachment.filename}</span>
			</div>
		{/if}
	{/each}
</div>

<style>
	.attachment-chips {
		display: flex;
		flex-wrap: wrap;
		justify-content: flex-end;
		gap: var(--space-sm);
	}

	.attachment-chip {
		display: inline-flex;
		align-items: center;
		gap: var(--space-xs);
		max-width: 640px;
		border: none;
		background: var(--color-surface-2);
		border-radius: var(--radius-full);
		padding: var(--space-xs) var(--space-md);
		font-size: 12px;
		color: var(--color-text-dim);
		box-shadow: var(--shadow-xs);
	}

	.attachment-chip span {
		overflow: hidden;
		text-overflow: ellipsis;
		white-space: nowrap;
	}

	.attachment-chip-link {
		text-decoration: none;
		cursor: pointer;
	}

	.attachment-chip-link:hover {
		background: var(--color-surface-3);
	}
</style>
