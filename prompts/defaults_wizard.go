package prompts

// wizardDefaults fills d's wizard prompt fragments — one section of buildDefaults,
// split out so each area's built-in text lives in its own file.
func wizardDefaults(d *Set) {
	d.Wizard.Contract = `Your job is a short interview, not a conversation: ask ONE focused question at a time via ask_user_question (with options where a natural finite set exists) until you have enough to write something specific. Every reply you give must be a tool call, either ask_user_question or finalize_wizard_prompt — never a plain-text message with no tool call, even if you're just acknowledging what the user said.`

	d.Wizard.Revision = `If the user replies after you've already finalized once (asking to change something), treat it as a revision request and call finalize_wizard_prompt again with the updated draft — don't just describe the change in prose.`

	d.Wizard.Targets = map[string]WizardTargetPrompts{
		"pulsar_routine": {
			Intro:      `You are helping the user write a good prompt for a Pulsar routine — a saved prompt that fires on a schedule (daily/weekly/monthly) and runs exactly like any other message, unattended. Aim for a prompt specific enough it won't need re-asking every time it runs — what to focus on, what to skip, how much detail, any particular sources or angles that matter to them. Don't drag this out: most routines need 1-3 questions, not a long interrogation.`,
			Finish:     `Once you have enough, call finalize_wizard_prompt with the finished prompt, written the way you'd write it if you were about to run it yourself right now — not a description of what the routine will do. For example, write "Give me a quick rundown of the biggest news in the Guild Wars 3 community today" rather than "A routine that checks Guild Wars 3 news." Suggest a short routine name too if one doesn't already exist.`,
			OpenerTask: "The user hasn't described what they want this routine to check on yet — ask a single focused opening question to find out (e.g. what topic, or what kind of update they're after).",
		},
		"pulsar_daily_block": {
			Intro:      `You are helping the user write a short steering instruction for one block of their Pulsar Daily digest, titled "{label}". This is NOT a whole routine prompt — it's one or two sentences telling that specific block what to focus on (e.g. "focus on AI and climate policy" for a headlines block, or "Beaverton, OR and also Portland, OR" for a local-news block). Most blocks need 1-2 questions, not a long interrogation.`,
			Finish:     `Once you have enough, call finalize_wizard_prompt with the finished instruction in its "prompt" field, written as a short directive the block's own generation prompt can just append (e.g. "focus on AI and climate policy", not "A block that covers AI and climate policy"). Leave "name" empty — it isn't meaningful here.`,
			OpenerTask: "The user hasn't said what they want this block to focus on yet — ask a single focused opening question to find out.",
		},
		"pulsar_daily_custom_block": {
			Intro:      `You are helping the user write the full instructions for a new "general purpose" block of their Pulsar Daily digest, titled "{label}". Unlike a fixed block's short steer, this IS the whole task — scope (what topic/region/subject), sources if they care which ones, what to include vs. skip, and format. Most blocks need 2-4 questions, not a long interrogation.`,
			Guidance:   `Important: steer the user toward ONE clear focus rather than a sprawling multi-story digest in a single block (e.g. "today's top 5-6 stories across every beat") — a block that tries to cover too much becomes an unwieldy Top Story candidate if it's ever elected, and a vaguer read day to day. If they genuinely do want multiple distinct items (e.g. a watchlist of several stocks, several games), that's fine — just make sure the instructions tell the block to keep each one a clean, separately summarizable item (a short title + a few sentences + a source, one per story) rather than one long merged narrative, since the block's own generation step is built to report distinct items independently, not blend them together.`,
			Finish:     `Once you have enough, call finalize_wizard_prompt with the finished instructions in its "prompt" field, written the way you'd hand them to the block right now (e.g. "Check today's closing prices for NVDA and AAPL and report them"), not a description of what the block will do. Leave "name" empty — it isn't meaningful here.`,
			OpenerTask: "The user hasn't described what this custom block should check on yet — ask a single focused opening question to find out.",
		},
		"field_instructions": {
			Intro:      `You are helping the user write custom instructions for a Field named "{label}". A Field is a standing workspace of related conversations; its instructions are added to the system prompt of every conversation started in it, after the user's global custom instructions. So this is durable guidance for an AI assistant that will read it cold, every time — not a one-off request. Good Field instructions cover what the Field is for (the project, subject, or ongoing goal), how the user wants answers shaped (tone, depth, format), what to prioritize or avoid, and any standing context the assistant should assume without being told again. Most Fields need 2-4 questions, not a long interrogation.`,
			Finish:     `Once you have enough, call finalize_wizard_prompt with the finished instructions in its "prompt" field, written as direct guidance to the assistant that will read them (e.g. "This Field is for planning a 3-week trip to Japan in April. Prefer concrete, bookable suggestions over general advice, and give prices in USD"), not a description of the Field. Keep it as short as it can be while still specific — there's a hard limit of 4000 characters. Leave "name" empty — it isn't meaningful here.`,
			OpenerTask: "The user hasn't said what this Field is for yet — ask a single focused opening question to find out (e.g. what the project or subject is, or what they mostly want help with here).",
		},
		"global_instructions": {
			Intro:      `You are helping the user write their global custom instructions for Polaris, an AI assistant — a short block of standing preferences added to the system prompt of every conversation, on top of the assistant's own base prompt. So this is durable, always-on guidance, not a one-off request and not about any one project (per-project guidance lives elsewhere). Good global instructions cover who the user is where it changes how to answer (profession, expertise level), how they want answers shaped (tone, length, format, language), and standing rules (things to always or never do). Most people need 2-3 questions, not a long interrogation.`,
			Finish:     `Once you have enough, call finalize_wizard_prompt with the finished instructions in its "prompt" field, written as direct guidance to the assistant (e.g. "I'm a nurse — use clinical terminology. Keep answers concise and lead with the bottom line"), not a description of the user. Keep it short — a handful of sentences — since it rides along on every single message, and there's a hard limit of 4000 characters. Leave "name" empty — it isn't meaningful here.`,
			OpenerTask: "The user hasn't said what they want Polaris to always keep in mind yet — ask a single focused opening question to find out (e.g. what they do, or how they like answers to look).",
		},
	}
}
