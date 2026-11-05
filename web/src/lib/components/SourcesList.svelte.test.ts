import { describe, it, expect, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';
import SourcesList from './SourcesList.svelte';
import type { Citation } from '$lib/types';

const cits: Citation[] = [
	{ title: 'Go Release History', url: 'https://go.dev/doc/devel/release', opened: true, words: 700, verified: true },
	{ title: 'Go 1.24 Release Notes', url: 'https://tip.golang.org/doc/go1.24', opened: true, words: 2760 },
	{ title: 'Whats new in Go 1.24?', url: 'https://reddit.com/r/golang/x' }
];

describe('SourcesList', () => {
	// Vitest globals are off, so testing-library's auto-cleanup never
	// registers; without this each test sees the previous one's DOM.
	afterEach(cleanup);

	it('shows a count + read/checked summary and keeps the sheet closed', () => {
		const { getByText, queryByRole } = render(SourcesList, { citations: cits });

		expect(getByText(/3\s+sources/)).toBeTruthy();
		expect(getByText('2 read · 1 checked')).toBeTruthy();
		expect(queryByRole('dialog')).toBeNull();
	});

	it('opens the sheet on click, with read-length meta and the verification note', async () => {
		const { getByRole, getByText } = render(SourcesList, { citations: cits });

		await fireEvent.click(getByRole('button', { name: /sources/i }));

		expect(getByRole('dialog')).toBeTruthy();
		expect(getByText('2 read in full · 1 from search results')).toBeTruthy();
		expect(getByText('go.dev · 4 min read')).toBeTruthy();
		// A source nothing read has no length to show — domain only.
		expect(getByText('reddit.com')).toBeTruthy();
		expect(getByText(/No mark doesn't mean the source is wrong/)).toBeTruthy();
	});

	it('closes on Escape and on the backdrop', async () => {
		const { getByRole, queryByRole, getByLabelText } = render(SourcesList, { citations: cits });

		await fireEvent.click(getByRole('button', { name: /sources/i }));
		await fireEvent.keyDown(window, { key: 'Escape' });
		expect(queryByRole('dialog')).toBeNull();

		await fireEvent.click(getByRole('button', { name: /sources/i }));
		await fireEvent.click(getByLabelText('Close', { selector: '.modal-backdrop-close' }));
		expect(queryByRole('dialog')).toBeNull();
	});

	it('omits the summary line and note when nothing was read or checked', async () => {
		const bare: Citation[] = [{ title: 'A', url: 'https://example.com/a' }];
		const { getByRole, queryByText } = render(SourcesList, { citations: bare });

		expect(queryByText(/read/)).toBeNull();
		await fireEvent.click(getByRole('button', { name: /source/i }));
		expect(queryByText(/No mark/)).toBeNull();
	});
});
