import {
	Zap,
	GraduationCap,
	Newspaper,
	Lightbulb,
	MessageCircleQuestion,
	Microscope,
	Binoculars
} from '@lucide/svelte';
import type { FocusMode } from './types';
import type { Component } from 'svelte';

export interface FocusModeOption {
	id: FocusMode;
	label: string;
	description: string;
	icon: Component;
}

// Shared by ComposerMenu.svelte (per-message picker) and SettingsPanel.svelte
// (standing default) — one list, not two copies that could drift out of
// sync. 'off' isn't included here; it's just what a mode resets to when
// tapped again / when no default is set, not a selectable entry itself.
export const FOCUS_MODES: FocusModeOption[] = [
	{ id: 'brief', label: 'Brief', description: 'Same research, shorter replies', icon: Zap },
	{
		id: 'researcher',
		label: 'Researcher',
		description: 'Cross-check sources, dig deeper',
		icon: Microscope
	},
	{ id: 'academic', label: 'Academic', description: 'Prefer academic sources', icon: GraduationCap },
	{ id: 'news', label: 'News', description: 'Prefer news sources', icon: Newspaper },
	{
		id: 'first_principles',
		label: 'First Principles',
		description: 'Reason up from fundamentals',
		icon: Lightbulb
	},
	{
		id: 'socratic',
		label: 'Socratic',
		description: 'Explore through guided questions',
		icon: MessageCircleQuestion
	},
	{
		id: 'safari',
		label: 'Safari',
		description: 'Immersive, interactive exploration',
		icon: Binoculars
	}
];
