package prompts

import "strings"

// WizardTargetPrompts is the part of a "help me write this" interview that
// differs per target — see Set.Wizard. Everything every target shares (the
// one-question-at-a-time contract, the revision rule) lives once on the
// Set, not repeated here; that repetition across four hand-copied system
// prompts is exactly what let them drift before this was consolidated.
//
// Intro and Finish may contain {label}, replaced with the WizardTarget's
// Label (a Daily block's title, a Field's name). Not a fmt verb: these are
// operator-edited YAML, where a stray "%" in prose would otherwise garble
// or panic a Sprintf.
type WizardTargetPrompts struct {
	// Intro says what's being written and how many questions it takes.
	Intro string `yaml:"intro"`
	// Guidance is an optional extra paragraph for a target that needs
	// steering beyond Intro/Finish (e.g. the custom Daily block's "don't
	// ask for a sprawling digest"). Blank for most targets.
	Guidance string `yaml:"guidance"`
	// Finish says what to hand to finalize_wizard_prompt and how to write it.
	Finish string `yaml:"finish"`
	// OpenerTask is the first user message when the caller has no draft to
	// seed the interview with.
	OpenerTask string `yaml:"opener_task"`
}

// HasWizardTarget reports whether kind names a target with prompts — how
// gateway/wizard.go rejects an unknown target with a 400 rather than
// starting an interview with an empty system prompt.
func (s *Set) HasWizardTarget(kind string) bool {
	_, ok := s.Wizard.Targets[kind]
	return ok
}

// WizardSystem assembles a target's full system prompt: Intro + the shared
// interview Contract as one paragraph, the optional Guidance, then Finish
// + the shared Revision rule as the closing paragraph. label fills
// {label}; blank falls back to "untitled" so a caller that forgot to send
// one reads as awkward rather than as a literal "{label}" in the prompt.
func (s *Set) WizardSystem(kind, label string) string {
	t := s.Wizard.Targets[kind]
	parts := []string{t.Intro + " " + s.Wizard.Contract}
	if t.Guidance != "" {
		parts = append(parts, t.Guidance)
	}
	parts = append(parts, t.Finish+" "+s.Wizard.Revision)

	if strings.TrimSpace(label) == "" {
		label = "untitled"
	}
	return strings.ReplaceAll(strings.Join(parts, "\n\n"), "{label}", label)
}

// WizardOpenerTask is the target's opening user message for a wizard with
// no seed draft.
func (s *Set) WizardOpenerTask(kind string) string {
	return s.Wizard.Targets[kind].OpenerTask
}

// fillWizardDefaults merges defaults into s's Wizard section per target
// per field, so a prompts.yaml overriding one target's Finish keeps the
// built-in text for everything else. Always builds a fresh Targets map:
// parsed's map is the file's own, and defaults' must never be aliased and
// then written through.
func fillWizardDefaults(s *Set) {
	if s.Wizard.Contract == "" {
		s.Wizard.Contract = defaults.Wizard.Contract
	}
	if s.Wizard.Revision == "" {
		s.Wizard.Revision = defaults.Wizard.Revision
	}
	merged := make(map[string]WizardTargetPrompts, len(defaults.Wizard.Targets))
	for kind, def := range defaults.Wizard.Targets {
		got := s.Wizard.Targets[kind]
		if got.Intro == "" {
			got.Intro = def.Intro
		}
		if got.Guidance == "" {
			got.Guidance = def.Guidance
		}
		if got.Finish == "" {
			got.Finish = def.Finish
		}
		if got.OpenerTask == "" {
			got.OpenerTask = def.OpenerTask
		}
		merged[kind] = got
	}
	s.Wizard.Targets = merged
}
