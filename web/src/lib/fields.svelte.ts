import type { Field, FieldDetail, FieldFile } from './types';

// The tag colors a field can wear — a curated, well-spread subset of
// app.css's 32 --color-cat-* tokens (Constellation's category palette),
// not all of them: a picker with 32 near-neighbor hues is worse than one
// with seven you can actually tell apart. Reusing that palette means no new
// tokens and colors already tuned for both themes (docs/plans/fields.md,
// "Color tag"). Order is display order in the picker.
export const FIELD_COLORS = [
	'technology',
	'space-astronomy',
	'history',
	'music',
	'art-design',
	'business-economy',
	'education-learning'
] as const;

// fieldColorVar resolves a field's stored color to a CSS value, or null
// for "no tag" — an unknown suffix (a palette that changed since it was
// saved) also resolves to null rather than an undefined variable, which
// would render as an invisible/initial color.
export function fieldColorVar(color: string): string | null {
	return (FIELD_COLORS as readonly string[]).includes(color) ? `var(--color-cat-${color})` : null;
}

// Result keeps a failed call's server message (the API answers plain-text
// 400s like "unknown default_model") available to show, without this module
// importing appState for its toast — appState imports this module, so that
// would be a cycle.
export type Result<T> = { ok: true; data: T } | { ok: false; error: string };

async function failure(res: Response): Promise<{ ok: false; error: string }> {
	const text = (await res.text().catch(() => '')).trim();
	return { ok: false, error: text || `Request failed (${res.status})` };
}

const NETWORK_ERROR = { ok: false, error: "Couldn't reach the server" } as const;

// FieldsState is Fields' own store — same shape as PulsarState/
// SettingsState: a dedicated class rather than more AppState, since a
// field's data (the list, one field's files/threads) is a separate
// concern from the open chat thread.
export class FieldsState {
	fields = $state<Field[]>([]);
	// Set once the list has resolved at least once — distinguishes "still
	// loading" from "you have no fields" (the hub's empty state).
	loaded = $state(false);
	// Flagged instead of clearing `fields`, same convention as
	// pulsarState's *Error fields: a network blip shouldn't wipe a list
	// someone is looking at (this app is mostly used from a phone over
	// Tailscale).
	error = $state(false);

	// Only favorited fields appear in the sidebar — an unfavorited one is
	// reachable solely through /fields (docs/plans/fields.md, "Pinning
	// to the sidebar"), same mechanism threads.favorite already is.
	favorites = $derived(this.fields.filter((p) => p.favorite));

	byId(id: string | null | undefined): Field | undefined {
		return id ? this.fields.find((p) => p.id === id) : undefined;
	}

	async load() {
		this.error = false;
		try {
			const res = await fetch('/api/fields');
			if (!res.ok) throw new Error(String(res.status));
			this.fields = (await res.json()) as Field[];
			this.loaded = true;
		} catch {
			this.error = true;
		}
	}

	// replaceInList swaps in a server-returned field so every view of the
	// list (hub, sidebar, header pill) reflects an edit at once, keeping the
	// server's recency order: an edited field floats to the top, matching
	// ListFields' updated_at DESC.
	private replaceInList(p: Field) {
		this.fields = [p, ...this.fields.filter((x) => x.id !== p.id)];
	}

	async create(input: Partial<Field> & { name: string }): Promise<Result<Field>> {
		try {
			const res = await fetch('/api/fields', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(input)
			});
			if (!res.ok) return await failure(res);
			const p = (await res.json()) as Field;
			this.replaceInList(p);
			return { ok: true, data: p };
		} catch {
			return NETWORK_ERROR;
		}
	}

	async update(id: string, patch: Partial<Field>): Promise<Result<Field>> {
		try {
			const res = await fetch(`/api/fields/${id}`, {
				method: 'PATCH',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(patch)
			});
			if (!res.ok) return await failure(res);
			const p = (await res.json()) as Field;
			this.replaceInList(p);
			return { ok: true, data: p };
		} catch {
			return NETWORK_ERROR;
		}
	}

	async remove(id: string): Promise<Result<null>> {
		try {
			const res = await fetch(`/api/fields/${id}`, { method: 'DELETE' });
			if (!res.ok) return await failure(res);
			this.fields = this.fields.filter((p) => p.id !== id);
			return { ok: true, data: null };
		} catch {
			return NETWORK_ERROR;
		}
	}

	async loadDetail(id: string): Promise<Result<FieldDetail>> {
		try {
			const res = await fetch(`/api/fields/${id}`);
			if (!res.ok) return await failure(res);
			const detail = (await res.json()) as FieldDetail;
			// Keep the list's copy fresh too (thread_count, updated_at) — the
			// header pill and sidebar read from it, not from this detail.
			this.fields = this.fields.map((p) => (p.id === id ? detail.field : p));
			return { ok: true, data: detail };
		} catch {
			return NETWORK_ERROR;
		}
	}

	async uploadFile(id: string, file: File): Promise<Result<FieldFile>> {
		try {
			const form = new FormData();
			form.append('file', file);
			const res = await fetch(`/api/fields/${id}/files`, { method: 'POST', body: form });
			if (!res.ok) return await failure(res);
			return { ok: true, data: (await res.json()) as FieldFile };
		} catch {
			return NETWORK_ERROR;
		}
	}

	async deleteFile(id: string, name: string): Promise<Result<null>> {
		try {
			const res = await fetch(`/api/fields/${id}/files/${encodeURIComponent(name)}`, {
				method: 'DELETE'
			});
			if (!res.ok) return await failure(res);
			return { ok: true, data: null };
		} catch {
			return NETWORK_ERROR;
		}
	}

	// moveThread files a thread under a field, or out of any with null.
	// The counts on the list's cards go stale on a move, so the caller
	// reloads the list on success rather than this guessing at both sides.
	async moveThread(threadId: string, fieldId: string | null): Promise<Result<null>> {
		try {
			const res = await fetch(`/api/threads/${threadId}/field`, {
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ field_id: fieldId })
			});
			if (!res.ok) return await failure(res);
			void this.load();
			return { ok: true, data: null };
		} catch {
			return NETWORK_ERROR;
		}
	}
}

export const fieldsState = new FieldsState();
