import { describe, it, expect } from 'vitest';
import { render } from '@testing-library/svelte';
import ToolEvent from './ToolEvent.svelte';
import type { TimelineItem } from '$lib/types';

// Commentary is text that streamed in live as the answer and was then
// re-classified when the model called a tool (turnEvents.ts). It must render
// ui fences as blocks, or a block the user watched build up collapses into its
// raw JSON the moment a tool call arrives.
describe('ToolEvent — commentary', () => {
	it('renders a ```ui fence as a block, not a JSON code block', () => {
		const item: TimelineItem = {
			kind: 'commentary',
			content:
				'Here is a look.\n\n```ui\n{"c":"callout","tone":"note","text":"Under 400 pages."}\n```\n\nLet me check.'
		};
		const { container, getByText } = render(ToolEvent, { item });

		expect(getByText('Under 400 pages.')).toBeTruthy();
		expect(container.querySelector('pre')).toBeNull();
		expect(container.textContent).not.toContain('"c":"callout"');
	});

	it('still renders ordinary Markdown and plain code fences', () => {
		const item: TimelineItem = {
			kind: 'commentary',
			content: 'Some **bold** text.\n\n```go\nfmt.Println("hi")\n```\n'
		};
		const { container } = render(ToolEvent, { item });

		expect(container.querySelector('strong')?.textContent).toBe('bold');
		expect(container.querySelector('pre code')).not.toBeNull();
	});
});
