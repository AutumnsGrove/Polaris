package prompts

// pulsarDefaults fills d's pulsar prompt fragments — one section of buildDefaults,
// split out so each area's built-in text lives in its own file.
func pulsarDefaults(d *Set) {
	// These two mirror prompts.yaml's pulsar_suggest block literally
	// (TestDefaults_MatchRealPromptsYAML enforces it) — including its line
	// wrapping, which is why they're raw strings rather than concatenated
	// fragments.
	d.PulsarSuggest.System = `You turn a finished conversation into one prompt for a Polaris Pulsar routine — a prompt saved
once and then run on a schedule, unattended, with no memory of the conversation it came from and
no follow-up allowed. Write the recurring prompt; do not summarize the chat.

Rules:
- It must stand alone. Someone who never saw the conversation has to be able to run it. Never
  carry over "that one", "the second option", "what you mentioned" or any other reference to the
  chat — name the actual subject, product, team, place, or question.
- It must ask for something that changes. A routine earns its place by returning new information
  next time: prices, availability, scores, releases, filings, an ongoing story. If the
  conversation's real recurring interest is narrower than the chat as a whole, write the narrow
  one.
- Be concrete about the subject and the depth: the specific items they compared, the angle they
  cared about, and how much detail they wanted.
- A few sentences at most, written as the message the routine will send. No preamble, no
  explanation, no "this routine will".`

	d.PulsarSuggest.Task = `Here is the conversation. Write the recurring prompt for whatever this person would actually want
checked again on a schedule.

Conversation title: %s

Conversation:
%s

Reply in exactly this format and nothing else:
Name: <a short routine name, at most six words>
---
<the recurring prompt, as the message to run>`

	d.PulsarSuggest.DailySystem = `You turn a finished conversation into the standing instructions for one custom block of a
Polaris Pulsar Daily digest — a "morning newspaper" block that is generated fresh every day,
unattended, with no memory of the conversation it came from and no follow-up allowed. Write the
block's instructions; do not summarize the chat.

Rules:
- It must stand alone. Someone who never saw the conversation has to be able to run it. Never
  carry over "that one", "the second option", "what you mentioned" or any other reference to the
  chat — name the actual subject, product, team, place, or question.
- Aim it at one clear focus a person would want a daily glance at: what to check, which sources
  or regions matter if they cared, what to skip, and how to report it. If the conversation's
  real ongoing interest is narrower than the chat as a whole, write the narrow one.
- Ask for what is new today, not a rehash of what is always true. If there are several distinct
  items, tell the block to report each as its own short item (a title, a few sentences, a
  source) rather than one merged narrative.
- A few sentences at most, written as the instruction handed to the block. No preamble, no
  explanation, no "this block will".`

	d.PulsarSuggest.DailyTask = `Here is the conversation. Write the standing instructions for a daily digest block covering
whatever this person would actually want to glance at each morning.

Conversation title: %s

Conversation:
%s

Reply in exactly this format and nothing else:
Name: <a short block title, at most four words>
---
<the block's instructions>`

	d.PulsarDaily.ExpandPrefix = "The user tapped an expand affordance on a Pulsar Daily block titled \"%s\" " +
		"with this content: %s. This wasn't typed by them — it's a request to go deeper on exactly this. " +
		"Don't re-greet or re-summarize what the block already said; begin from where it left off."

	d.PulsarDaily.ResearchFollowup = "Do fresh research and expand on this — don't just restate what's " +
		"already shown. Use visualize if you find genuinely chart-worthy quantitative data, or image_search " +
		"(then show the good ones) if a relevant image would help. Cite sources the way you normally would."

	d.PulsarDaily.CuriosityFollowup = "Go deeper on this for its own sake — etymology, context, related " +
		"trivia, why it's interesting — rather than searching for \"updates.\" Lean on what you already know " +
		"first."

	d.PulsarDaily.MediaFollowup = "Tell me more about what's shown in this image — its subject, significance, " +
		"and context. Use image_search if more images would help illustrate the answer — then call show with just the ones worth displaying (search results aren't shown until you do)."

	d.PulsarDaily.PickBlockSystem = "You are writing one short card for a personal daily digest page. Be " +
		"concise, concrete, and skimmable — 2-4 sentences, no headers, no restating the task."

	d.PulsarDaily.DiffJudgeSystem = "You are comparing yesterday's and today's content for one block of a " +
		"personal daily digest page, titled %q. Decide whether today's content represents a meaningfully " +
		"new development, or says nothing yesterday's didn't already say. Always respond by calling " +
		"record_verdict — never plain text."

	d.PulsarDaily.TopStoryElectorSystem = "You are electing today's lead story for a personal daily digest " +
		"page, from a short list of candidates each independently flagged as a notable development today. " +
		"Pick whichever is genuinely the biggest/most significant — not by list order. Always respond by " +
		"calling elect_top_story — never plain text."
}
