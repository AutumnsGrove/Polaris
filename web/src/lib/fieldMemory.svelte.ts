import type { Memory } from './settings.svelte';
import type { MemorySource } from './memorySource';

// One Field's own memory store as a MemorySource (issue #133). Same three
// calls SettingsState makes for global memory, against the Field-scoped
// routes (gateway/field_memory_routes.go). One instance per Field page view;
// the page remounts it when switching Fields.
export class FieldMemorySource implements MemorySource {
	memories = $state<Memory[]>([]);
	loaded = $state(false);
	busy = $state(false);
	message = $state('');
	importBusy = $state(false);
	importMessage = $state('');

	constructor(private fieldId: string) {}

	private get base() {
		return `/api/fields/${this.fieldId}/memories`;
	}

	get exportHref() {
		return `${this.base}/export`;
	}

	// Same contract as SettingsState.importMemories: true only on a real
	// success, so the caller keeps a long pasted dump on failure.
	async importMemories(dump: string): Promise<boolean> {
		this.importBusy = true;
		this.importMessage = '';
		try {
			const res = await fetch(`${this.base}/import`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ dump })
			});
			if (!res.ok) {
				this.importMessage = "Couldn't parse that — try pasting the whole export again.";
				return false;
			}
			const data = await res.json();
			this.importMessage = data.message ?? '';
			this.memories = data.memories ?? this.memories;
			return true;
		} catch {
			this.importMessage = 'Could not reach the server — try again.';
			return false;
		} finally {
			this.importBusy = false;
		}
	}

	async load() {
		try {
			const res = await fetch(this.base);
			if (res.ok) this.memories = await res.json();
		} catch {
			// Best-effort — the list just shows its loading/empty state.
		} finally {
			this.loaded = true;
		}
	}

	async remove(name: string) {
		const res = await fetch(`${this.base}/${encodeURIComponent(name)}`, { method: 'DELETE' });
		if (res.ok) this.memories = this.memories.filter((m) => m.name !== name);
		return res.ok;
	}

	async instruct(instruction: string) {
		this.busy = true;
		this.message = '';
		try {
			const res = await fetch(`${this.base}/chat`, {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ instruction })
			});
			if (!res.ok) {
				this.message = "Couldn't process that — try rephrasing.";
				return;
			}
			const data = await res.json();
			this.message = data.message ?? '';
			this.memories = data.memories ?? this.memories;
		} catch {
			this.message = "Couldn't reach the server — try again.";
		} finally {
			this.busy = false;
		}
	}
}
