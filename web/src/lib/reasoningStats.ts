// Header stats for a reasoning block ("Thought for 12s · ~2.8k tokens").

// ~4 characters per token is the usual English rule of thumb. The count is
// an estimate on purpose: OpenRouter only reports real reasoning-token usage
// once per LLM call, after the burst is over and not split per burst, so a
// live-ticking figure has to be derived from the streamed text. The caller
// renders it with a leading "~" so nobody mistakes it for a metered number.
export function estimateReasoningTokens(content: string): number {
	return Math.round(content.length / 4);
}

export function formatDuration(ms: number): string {
	const secs = Math.max(0, Math.round(ms / 1000));
	if (secs < 60) return `${secs}s`;
	const mins = Math.floor(secs / 60);
	const rem = secs % 60;
	return rem === 0 ? `${mins}m` : `${mins}m ${rem}s`;
}

export function formatTokens(n: number): string {
	if (n < 1000) return `${n}`;
	// One decimal under 10k ("2.8k"), whole thousands above ("14k").
	return n < 10_000 ? `${(n / 1000).toFixed(1)}k` : `${Math.round(n / 1000)}k`;
}

// Returns '' when there's nothing worth showing yet (e.g. a legacy row with
// no persisted duration and no content), so the header omits the stats
// entirely rather than rendering a bare "Thought for".
export function reasoningStatsLabel(
	item: { content: string; done: boolean; startedAt?: number; durationMs?: number },
	now: number
): string {
	const elapsed = item.done ? item.durationMs : item.startedAt !== undefined ? now - item.startedAt : undefined;
	const parts: string[] = [];
	if (elapsed !== undefined) parts.push(`${item.done ? 'Thought' : 'Thinking'} for ${formatDuration(elapsed)}`);
	const tokens = estimateReasoningTokens(item.content);
	if (tokens > 0) parts.push(`~${formatTokens(tokens)} tokens`);
	return parts.join(', ');
}
