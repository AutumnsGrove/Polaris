import type { UploadedAttachment } from './types';

/**
 * Uploads a file to POST /api/upload ahead of sending the message that
 * references it — same two-step shape as push-to-talk voice memos (see
 * speech.ts/VoiceButton.svelte): upload first, then the returned id rides
 * along in the next ClientMessage. Returns null on failure; the caller
 * decides whether to fall back to sending the text alone.
 */
export async function uploadAttachment(file: File): Promise<UploadedAttachment | null> {
	const body = new FormData();
	body.append('file', file);

	const res = await fetch('/api/upload', { method: 'POST', body });
	if (!res.ok) {
		console.error('attachment upload failed', await res.text());
		return null;
	}
	return (await res.json()) as UploadedAttachment;
}
