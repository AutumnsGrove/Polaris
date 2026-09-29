// See https://svelte.dev/docs/kit/types#app.d.ts
// for information about these interfaces
declare global {
	namespace App {
		// interface Error {}
		// interface Locals {}
		// interface PageData {}
		// interface PageState {}
		// interface Platform {}
	}
}

// `autocorrect` is a real attribute (WebKit/iOS Safari honors it; the composer
// and text-edit fields set it to "off" so iOS doesn't rewrite what's typed) but
// svelte/elements' typings don't list it, which made svelte-check reject every
// <textarea autocorrect="off">. Declared here rather than dropping the
// attribute or spreading it past the type checker at each call site.
declare module 'svelte/elements' {
	interface HTMLAttributes<T> {
		autocorrect?: 'on' | 'off' | null | undefined;
	}
}

export {};
