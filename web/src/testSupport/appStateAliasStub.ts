// Never imported for real — see vitest.config.ts's resolve.alias comment.
// This exists only so Vite can resolve the bare "$app/state" specifier
// that real route files use; any test that actually mounts one of those
// routes replaces this via its own vi.mock('$app/state', ...).
export const page = { params: {} };
