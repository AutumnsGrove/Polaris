import type { Project, ProjectDetail, ProjectFile } from './types';

// The tag colors a project can wear — a curated, well-spread subset of
// app.css's 32 --color-cat-* tokens (Constellation's category palette),
// not all of them: a picker with 32 near-neighbor hues is worse than one
// with seven you can actually tell apart. Reusing that palette means no new
// tokens and colors already tuned for both themes (docs/plans/projects.md,
// "Color tag"). Order is display order in the picker.
export const PROJECT_COLORS = [
	'technology',
	'space-astronomy',
	'history',
	'music',
	'art-design',
	'business-economy',
	'education-learning'
] as const;

// projectColorVar resolves a project's stored color to a CSS value, or null
// for "no tag" — an unknown suffix (a palette that changed since it was
// saved) also resolves to null rather than an undefined variable, which
// would render as an invisible/initial color.
export function projectColorVar(color: string): string | null {
	return (PROJECT_COLORS as readonly string[]).includes(color) ? `var(--color-cat-${color})` : null;
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

// ProjectsState is Projects' own store — same shape as PulsarState/
// SettingsState: a dedicated class rather than more AppState, since a
// project's data (the list, one project's files/threads) is a separate
// concern from the open chat thread.
export class ProjectsState {
	projects = $state<Project[]>([]);
	// Set once the list has resolved at least once — distinguishes "still
	// loading" from "you have no projects" (the hub's empty state).
	loaded = $state(false);
	// Flagged instead of clearing `projects`, same convention as
	// pulsarState's *Error fields: a network blip shouldn't wipe a list
	// someone is looking at (this app is mostly used from a phone over
	// Tailscale).
	error = $state(false);

	// Only favorited projects appear in the sidebar — an unfavorited one is
	// reachable solely through /projects (docs/plans/projects.md, "Pinning
	// to the sidebar"), same mechanism threads.favorite already is.
	favorites = $derived(this.projects.filter((p) => p.favorite));

	byId(id: string | null | undefined): Project | undefined {
		return id ? this.projects.find((p) => p.id === id) : undefined;
	}

	async load() {
		this.error = false;
		try {
			const res = await fetch('/api/projects');
			if (!res.ok) throw new Error(String(res.status));
			this.projects = (await res.json()) as Project[];
			this.loaded = true;
		} catch {
			this.error = true;
		}
	}

	// replaceInList swaps in a server-returned project so every view of the
	// list (hub, sidebar, header pill) reflects an edit at once, keeping the
	// server's recency order: an edited project floats to the top, matching
	// ListProjects' updated_at DESC.
	private replaceInList(p: Project) {
		this.projects = [p, ...this.projects.filter((x) => x.id !== p.id)];
	}

	async create(input: Partial<Project> & { name: string }): Promise<Result<Project>> {
		try {
			const res = await fetch('/api/projects', {
				method: 'POST',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(input)
			});
			if (!res.ok) return await failure(res);
			const p = (await res.json()) as Project;
			this.replaceInList(p);
			return { ok: true, data: p };
		} catch {
			return NETWORK_ERROR;
		}
	}

	async update(id: string, patch: Partial<Project>): Promise<Result<Project>> {
		try {
			const res = await fetch(`/api/projects/${id}`, {
				method: 'PATCH',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify(patch)
			});
			if (!res.ok) return await failure(res);
			const p = (await res.json()) as Project;
			this.replaceInList(p);
			return { ok: true, data: p };
		} catch {
			return NETWORK_ERROR;
		}
	}

	async remove(id: string): Promise<Result<null>> {
		try {
			const res = await fetch(`/api/projects/${id}`, { method: 'DELETE' });
			if (!res.ok) return await failure(res);
			this.projects = this.projects.filter((p) => p.id !== id);
			return { ok: true, data: null };
		} catch {
			return NETWORK_ERROR;
		}
	}

	async loadDetail(id: string): Promise<Result<ProjectDetail>> {
		try {
			const res = await fetch(`/api/projects/${id}`);
			if (!res.ok) return await failure(res);
			const detail = (await res.json()) as ProjectDetail;
			// Keep the list's copy fresh too (thread_count, updated_at) — the
			// header pill and sidebar read from it, not from this detail.
			this.projects = this.projects.map((p) => (p.id === id ? detail.project : p));
			return { ok: true, data: detail };
		} catch {
			return NETWORK_ERROR;
		}
	}

	async uploadFile(id: string, file: File): Promise<Result<ProjectFile>> {
		try {
			const form = new FormData();
			form.append('file', file);
			const res = await fetch(`/api/projects/${id}/files`, { method: 'POST', body: form });
			if (!res.ok) return await failure(res);
			return { ok: true, data: (await res.json()) as ProjectFile };
		} catch {
			return NETWORK_ERROR;
		}
	}

	async deleteFile(id: string, name: string): Promise<Result<null>> {
		try {
			const res = await fetch(`/api/projects/${id}/files/${encodeURIComponent(name)}`, {
				method: 'DELETE'
			});
			if (!res.ok) return await failure(res);
			return { ok: true, data: null };
		} catch {
			return NETWORK_ERROR;
		}
	}

	// moveThread files a thread under a project, or out of any with null.
	// The counts on the list's cards go stale on a move, so the caller
	// reloads the list on success rather than this guessing at both sides.
	async moveThread(threadId: string, projectId: string | null): Promise<Result<null>> {
		try {
			const res = await fetch(`/api/threads/${threadId}/project`, {
				method: 'PUT',
				headers: { 'Content-Type': 'application/json' },
				body: JSON.stringify({ project_id: projectId })
			});
			if (!res.ok) return await failure(res);
			void this.load();
			return { ok: true, data: null };
		} catch {
			return NETWORK_ERROR;
		}
	}
}

export const projectsState = new ProjectsState();
