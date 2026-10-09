package prompts

// oracleDefaults fills d's oracle prompt fragments — one section of buildDefaults,
// split out so each area's built-in text lives in its own file.
func oracleDefaults(d *Set) {
	d.Oracle.Section = "## Oracle\n\n" +
		"These notes come from an automatic pre-read of the user's message, not from the user. Treat\n" +
		"them as hints about what probably helps here — follow them when they fit, and ignore any that\n" +
		"turn out not to match what the user actually asked.\n\n{items}"
	d.Oracle.QuestionPreamble = "You are reading a message someone sent to a research assistant that " +
		"searches the web and cites sources. Answer about the latest message; an earlier message, if " +
		"present, is only context."
	d.Oracle.LinkNudge = "The message contains a link. Read it with web_read (or youtube_transcript for a " +
		"YouTube video) before answering, rather than guessing from the URL text, and base the answer on what " +
		"the page actually says."
	d.Oracle.Checks = map[string]OracleCheck{
		"focus": {
			Instructions: "Which answering style best fits this message? Pick \"off\" unless one style " +
				"is clearly a better fit than a normal, balanced answer.",
			Options: map[string]string{
				"off":              "A normal balanced answer fits; no special style is clearly better.",
				"brief":            "The message itself asks for a short answer (quick question, tl;dr, one word) or is a single fact lookup.",
				"researcher":       "The question needs careful cross-checking of several sources, or has real consequences if wrong.",
				"academic":         "A scientific, medical, or technical question best answered from papers, journals, or official documentation.",
				"news":             "About a current or recent event, where fresh news coverage matters more than reference pages.",
				"shopper":          "The person wants to find, compare, or buy a product.",
				"first_principles": "The person wants to understand why or how something works from the ground up.",
				"socratic":         "The person wants to be guided to work something out themselves, not handed the answer.",
				"safari":           "The person explicitly wants to explore a broad topic interactively, stop by stop, over several turns.",
			},
		},
		"research": {
			Instructions: "Does answering this well require searching the web or reading current information?",
			Options: map[string]string{
				"yes": "Needs current facts, specifics, prices, news, anything that could have changed recently, " +
					"or a specific checkable fact about a real person/date/event (ages, release dates, " +
					"statistics) — those are easy to get subtly wrong from memory alone.",
				"no": "Casual conversation, writing help, brainstorming, or genuinely stable general knowledge.",
			},
			Inject: map[string]string{
				"no": "This message probably doesn't need a web search — it looks answerable from general " +
					"knowledge or the conversation so far. Answer directly unless you find you're unsure of a " +
					"specific fact, in which case search as normal.",
			},
		},
		"high_stakes": {
			Instructions: "Would acting on a wrong answer to this message risk someone's health, legal standing, " +
				"money, or physical safety?",
			Options: map[string]string{
				"none":      "Low stakes; a wrong answer would be an inconvenience at most.",
				"medical":   "Health, symptoms, medications, dosages, diagnoses, or treatment.",
				"legal":     "Laws, rights, contracts, disputes, taxes as a legal matter, or legal procedure.",
				"financial": "Investing, debt, taxes, insurance, large purchases, or other money decisions.",
				"safety":    "Physical danger — electrical, chemical, structural, vehicles, weapons, outdoor risks.",
			},
			Inject: map[string]string{
				"medical": "This looks like a medical question. Prefer primary clinical sources — government health " +
					"agencies, peer-reviewed research, professional society guidance, drug labels — over " +
					"health blogs or forums. Be exact about dosages, thresholds, and who a finding applies to. " +
					"Say plainly where evidence is weak or mixed, and when something warrants seeing a " +
					"clinician, say so once, clearly, without burying the answer in disclaimers.",
				"legal": "This looks like a legal question. Law depends on jurisdiction — if the user's isn't clear " +
					"from context or memory, say which one your answer assumes. Prefer statute text, court or " +
					"government sources, and bar-association guidance over general-audience summaries. Note " +
					"when an answer turns on specific facts a lawyer would need to see.",
				"financial": "This looks like a financial decision. Prefer primary sources (regulators, official rate " +
					"and tax tables, fund prospectuses, company filings) over promotional content, and check " +
					"that numbers are current. Separate facts from opinion, and name the assumptions any " +
					"recommendation depends on.",
				"safety": "This involves physical safety. Prefer manufacturer documentation, official codes and " +
					"standards, and safety agencies. State the specific hazard and the specific precaution " +
					"rather than a generic warning, and don't give confident instructions for anything you " +
					"couldn't source.",
				"any": "When sources disagree on something that matters here, use compare_sources rather than " +
					"picking one.",
			},
			ByFocus: map[string]map[string]string{
				"brief": {
					"any": "This looks like a {option} question. Keep the answer short as asked, but brevity trims " +
						"explanation, not safety: keep any figure exact, never drop the one caveat that changes " +
						"what the user should do, and still cite a primary source for it.",
				},
			},
		},
		"intent": {
			Instructions: "What kind of thing is this message mainly asking about?",
			Options: map[string]string{
				"general":        "None of the other options clearly fits.",
				"place":          "A place, business, restaurant, or something nearby or at a specific location.",
				"book":           "A book, author, or what to read.",
				"film_tv":        "A movie, TV show, actor, or what to watch.",
				"music":          "A song, album, artist, or what to listen to.",
				"product":        "A specific product or buying decision.",
				"weather":        "Weather or a forecast.",
				"video":          "A specific YouTube video or its contents.",
				"code":           "A code repository, library, or programming project.",
				"definition":     "The meaning, pronunciation, or origin of a word.",
				"academic_paper": "A specific research paper, study, or preprint, or what the research says on a narrow topic.",
				"image":          "The person wants to see photos or pictures of something.",
				"person_org":     "A specific person, company, or organization — who they are, what they did, background.",
				"recipe":         "A recipe, cooking technique, ingredient substitution, or food question.",
				"travel":         "Planning a trip — destinations, routes, itineraries, visas, or what to do somewhere.",
				"sports":         "A sports score, schedule, standing, roster, or result.",
				"event":          "When something happens — a release date, showtimes, an event, a deadline, a schedule.",
				"datetime":       "The current time or date somewhere, a time zone conversion, or time until or since something.",
			},
			Inject: map[string]string{
				"place": "This is about a place. nearby_search gives real listings with addresses, hours, and " +
					"ratings — use it rather than relying on web_search alone, and include those specifics.",
				"book": "This is about books. Use the books tool for real bibliographic data. If more than one " +
					"title fits, briefly say how they differ (focus, tone, audience, depth) so the choice is " +
					"easy, rather than just listing them. " +
					"Its results aren't shown automatically — put your picks on screen with highlight (image_index + a short why).",
				"film_tv": "This is about film or TV. Use the movies tool for real details (year, cast, runtime, " +
					"where it's streaming if available). If recommending several, say what distinguishes each. " +
					"Its results aren't shown automatically — put your picks on screen with highlight (image_index + a short why).",
				"music": "This is about music. Use the music tool for real release and artist data. If recommending " +
					"several, say what distinguishes each. " +
					"Its results aren't shown automatically — put your picks on screen with highlight (image_index + a short why).",
				"product": "This is about a product. Compare real, currently available options with actual prices " +
					"and the tradeoffs that matter for this use — not a generic feature list.",
				"weather": "This is about weather. Use the weather tool rather than a web search.",
				"video": "This refers to a video. If there's a YouTube link or an identifiable video, read its " +
					"transcript with youtube_transcript rather than guessing at its contents.",
				"code": "This is about a code project. github_repo and github_activity give real, current repo " +
					"data — prefer them over search results about the project.",
				"definition": "This is about a word. Use the dictionary tool for the definition, and mention usage " +
					"or origin if it's interesting.",
				"academic_paper": "This is about a specific paper or body of research. reference_lookup can fetch an arXiv abstract directly — prefer it over a general search, then cite the paper itself (authors, year, venue) rather than a blog post about it, and say how strong the evidence is.",
				"image":          "The user wants to see pictures. image_search returns real photos with their source pages — use it rather than describing the thing in words. Its results aren't shown automatically: check the promising ones with view_image, then display only the good ones with show (image_indices, a photo gallery) or highlight (image_index cards, when the user is choosing between things).",
				"person_org":     "This is about a specific person or organization. Prefer their own site, filings, and established reporting over aggregator profiles, and be careful to match the right entity when a name is shared. State dates for anything that changes (roles, ownership).",
				"recipe":         "This is a cooking question. Prefer recipes from sources that test and explain them, note quantities and timings exactly, and say what a substitution will change.",
				"travel":         "This is about travel. Rules, prices, and hours change — prefer official tourism, transit, and government sources, say when a detail was last verified, and use nearby_search for real places along the way.",
				"sports":         "This is about sports. Prefer the league's or team's official site or a live-scores source, and always state the as-of time, since scores and standings go stale within hours.",
				"event":          "This is about when something happens. Prefer the organizer's or official announcement, state the date with its time zone, and flag anything still tentative or unconfirmed.",
				"datetime":       "This depends on the time or date. Call current_time for the exact time rather than guessing, and use calculator's days_between for date spans.",
			},
			ByFocus: map[string]map[string]string{
				"brief": {
					"book":    "This is about books. Use the books tool; give the one best pick, or one line per title on how they differ.",
					"film_tv": "This is about film or TV. Use the movies tool; give the one best pick, or one line per title on how they differ.",
					"music":   "This is about music. Use the music tool; give the one best pick, or one line per title on how they differ.",
					"product": "This is about a product. Give the single best current option with its real price.",
				},
			},
		},
		"clarify": {
			Instructions: "Is this message ambiguous enough that the answer would be substantially different " +
				"depending on something the person didn't say — so that asking one question first would clearly " +
				"save wasted research?",
			Options: map[string]string{
				"no":  "Clear enough to answer well, or any ambiguity has an obvious default.",
				"yes": "Two or more very different readings, or a missing detail that changes everything.",
			},
			Inject: map[string]string{
				"yes": "This message looks ambiguous in a way that matters. Before researching, ask one short " +
					"clarifying question with ask_user_question, offering the two or three likeliest readings " +
					"as options. Skip this if, on reflection, the conversation or memory already answers it.",
			},
		},
		"recall": {
			Instructions: "Does this message refer back to an earlier conversation the person had with the " +
				"assistant (for example \"like we talked about\", \"that thing from last week\", \"remember when\")?",
			Options: map[string]string{
				"no":  "No reference to a past conversation.",
				"yes": "Explicitly or clearly refers to something discussed before.",
			},
			Inject: map[string]string{
				"yes": "The user seems to be referring to an earlier conversation. If memory doesn't already " +
					"cover it, use search_chats to find it before answering rather than asking them to repeat " +
					"themselves.",
			},
		},
		"format": {
			Instructions: "What shape of answer would serve this message best? Pick \"none\" unless one clearly beats ordinary well-organized prose for what was asked.",
			Options: map[string]string{
				"none":       "No particular shape is clearly better; the model's own judgment is fine.",
				"table":      "Several items compared across the same attributes (specs, prices, pros/cons, options side by side).",
				"comparison": "The person is choosing between specific options and wants to know which to pick.",
				"steps":      "A procedure or how-to where order matters.",
				"list":       "A set of discrete items — recommendations, examples, ideas — with no strong ordering.",
				"prose":      "An explanation, story, or reasoning that reads best as connected paragraphs, not fragments.",
				"code":       "The answer is mostly code, a command, or a config snippet.",
				"timeline":   "A sequence of events over time, a history, or a schedule.",
			},
			Inject: map[string]string{
				"table":      "This suits a table: one row per item, one column per attribute that actually matters for the decision, with real values in the cells. Add a line after it on what the table shows, not a restatement of it.",
				"comparison": "The user is choosing between options. Lay out how they differ on the things that matter for their use, then end with a clear pick and the reason for it — and what would change that pick — rather than leaving the choice open.",
				"steps":      "This is a procedure. Give numbered steps in order, each one doing one thing, with exact commands, values, or settings where they apply, and say up front what's needed first.",
				"list":       "A short list fits. Keep each item to a line or two and lead with the item itself, not preamble.",
				"prose":      "This reads best as connected prose, not a bulleted breakdown — explain the reasoning in paragraphs and keep headings and lists out of it.",
				"code":       "Lead with the code or command in a fenced block, then only as much explanation as it needs.",
				"timeline":   "Present this in chronological order with dates, so the sequence and the gaps between events are easy to see.",
			},
		},
		// Prism (docs/plans/intelligent-ui.md): would a structured visual block
		// beat prose? Only the shapes the renderer can draw are offered; each
		// option's nudge names the block and gives its exemplar line, which is
		// the just-in-time few-shot that backs up the base prompt's grammar.
		"ui": {
			Instructions: "Would a structured visual block serve this message clearly better than ordinary prose? " +
				"Pick \"none\" unless the answer is really one of these shapes and prose would be harder to scan.",
			Options: map[string]string{
				"none":      "Prose, a short list, or code serves this best.",
				"compare":   "Choosing between specific options across shared attributes.",
				"steps":     "A procedure where order matters.",
				"choose":    "The person asks which to pick or what to do, and the best answer depends on their own situation (usage, budget, location, goals), so a few if-then rules serve them better than a table of attributes.",
				"checklist": "Things to prepare, pack or tick off.",
				"timeline":  "A sequence of dated events, a history, or a schedule. Not a price, number or trend over time.",
				"procon":    "One thing weighed for and against.",
				"facts":     "An at-a-glance summary of one named thing (a product, place, person or organization).",
			},
			Inject: map[string]string{
				"compare": "The user is choosing between options. A compare block fits: write one ui fence with " +
					"{\"c\":\"compare\",\"cols\":[\"A\",\"B\"],\"pick\":0} then one {\"row\":\"Price\",\"v\":[\"...\",\"...\"]} " +
					"line per attribute, then say which to pick and what would change that. Keep the prose short.",
				"steps": "This is a procedure where order matters. A steps block fits: write one ui fence with " +
					"{\"c\":\"steps\",\"title\":\"...\"} then one {\"i\":\"Step\",\"d\":\"detail\",\"t\":\"2 min\"} line per step " +
					"(leave out t unless it is a real duration), and say up front what is needed first.",
				"choose": "The right answer depends on the person's situation. A choose block fits: write one ui fence " +
					"with {\"c\":\"choose\",\"title\":\"...\"} then one {\"if\":\"their situation\",\"then\":\"the pick\"} line " +
					"per case, then say what you would need to know to narrow it down. Keep the prose short.",
				"checklist": "This is a list of things to prepare or tick off. A checklist block fits: write one ui fence " +
					"with {\"c\":\"checklist\",\"title\":\"...\"} then one {\"i\":\"Item\"} line per item, and keep any " +
					"caveat that matters in the prose.",
				"timeline": "This is events over time. A timeline block fits: write one ui fence with {\"c\":\"timeline\"} " +
					"then one {\"when\":\"1969\",\"i\":\"What happened\"} line per event, in time order, and put the " +
					"context in the prose around it.",
				"procon": "One thing is being weighed. A procon block fits: write one ui fence with {\"c\":\"procon\"} " +
					"then {\"+\":\"point\"} and {\"-\":\"point\"} lines, then give your overall read in the prose.",
				"facts": "The user wants the essentials on one named thing. A facts block fits: write one ui fence " +
					"with {\"c\":\"facts\",\"title\":\"Name\",\"sub\":\"what it is\"} then one {\"k\":\"Label\",\"v\":\"value\"} " +
					"line per fact, cited where you can.",
			},
		},
		"depth": {
			Instructions: "How much detail does this message call for? Pick \"standard\" unless it clearly wants something noticeably shorter or more thorough than a normal answer.",
			Options: map[string]string{
				"standard": "A normal-length answer fits.",
				"quick":    "A casual, low-effort question where a couple of sentences is the right size.",
				"thorough": "A complex or open-ended question where a short answer would leave out what matters.",
			},
			Inject: map[string]string{
				"quick":    "This is a quick question — answer it in a few sentences and stop. Add a source, not an essay.",
				"thorough": "This warrants a thorough answer: cover the parts that change the conclusion, organize it with clear headings, and cite as you go. Depth means completeness, not padding.",
			},
		},
		"recency": {
			Instructions: "How much does the age of the information matter for answering this well?",
			Options: map[string]string{
				"evergreen": "Stable knowledge; older sources are fine.",
				"recent":    "Things that change over months — software versions, prices, rankings, \"best X\", policies.",
				"breaking":  "Something happening now or in the last few days — news, live events, scores, outages.",
			},
			Inject: map[string]string{
				"recent":   "Freshness matters here. Search with web_search's recency set to \"year\" (widen or drop it if that comes back empty), check the date on what you cite, and state an \"as of\" date for anything that could have changed since.",
				"breaking": "This is time-sensitive. Search with web_search's recency set to \"week\" (or \"day\" if it is about today; widen it if that comes back empty), prefer the most recent coverage and the primary source of record, and lead with what is confirmed as of now versus still developing. Say plainly how fresh your newest source is.",
			},
		},
		"source_type": {
			Instructions: "What kind of source would answer this best? Pick \"any\" unless one kind is clearly better than the general web.",
			Options: map[string]string{
				"any":          "General sources are fine.",
				"primary_docs": "Official documentation, specifications, manuals, or a company's own published material.",
				"community":    "Real-world experience — \"is it worth it\", \"what's it actually like\", reliability, common problems.",
				"official":     "A government, regulator, standards body, or other institution is the authority.",
				"academic":     "Peer-reviewed research, journals, or preprints.",
			},
			Inject: map[string]string{
				"primary_docs": "Go to the primary documentation — the vendor's docs, the spec, the manual — before secondary write-ups, and cite the page itself.",
				"community":    "This is about lived experience, which official pages won't tell you. Look at forums, discussion threads, and owner reviews, and report the pattern across several rather than one loud opinion. Say plainly that it's anecdotal.",
				"official":     "The authority here is an institution. Cite the agency's, regulator's, or standards body's own page, and note its publication or revision date.",
				"academic":     "Prefer peer-reviewed papers and reputable preprints over news coverage of them. Say what the study actually measured, how large or strong the effect was, and where the evidence is thin.",
			},
		},
		"contested": {
			Instructions: "Is this a topic where informed people or credible sources genuinely disagree — politics, social or moral questions, or actively disputed science — rather than a matter of fact?",
			Options: map[string]string{
				"no":  "A factual question, or one with broad agreement.",
				"yes": "A genuinely contested question where reasonable people or credible sources disagree.",
			},
			Inject: map[string]string{
				"yes": "This topic is genuinely contested. Present the main positions as their own proponents would state them, sourced to people or institutions who hold them, keep what is factual separate from what is a value judgment, and avoid steering toward a side. If the user asks for your view, say what the evidence does and doesn't settle.",
			},
		},
		"claim_check": {
			Instructions: "Is the person asking whether something they heard, read, or saw is true — a rumor, a viral claim, a screenshot, a \"supposedly\", or \"is it true that\"?",
			Options: map[string]string{
				"no":  "Not a request to verify a claim.",
				"yes": "Asks whether a specific claim is true.",
			},
			Inject: map[string]string{
				"yes": "The user wants a claim checked. Find where it originated and check that original source rather than repeating what others say about it. Give a clear verdict — true, false, misleading, or unverified — say what the evidence is, and if it's partly true, say which part.",
			},
		},
		"locale": {
			Instructions: "Does the right answer depend on where the person is — a country, state, city, or region — such as laws, prices, availability, regulations, or local services?",
			Options: map[string]string{
				"no":  "The answer is the same everywhere.",
				"yes": "The answer varies by place, and the message doesn't make the place fully clear.",
			},
			Inject: map[string]string{
				"yes": "The answer depends on location. Use memory for where the user is if it's there; otherwise say which region your answer assumes, and note what would differ elsewhere. Prefer local sources over ones written for another country.",
			},
		},
		"task": {
			Instructions: "What is the person trying to do? Pick \"answer\" for an ordinary question.",
			Options: map[string]string{
				"answer":       "An ordinary question wanting a fact or an answer.",
				"explain":      "They want to understand something — how or why it works.",
				"decide":       "They're weighing a choice and want help making it.",
				"plan":         "They want a plan, schedule, itinerary, or sequence of steps to achieve something.",
				"troubleshoot": "Something is broken or misbehaving and they want it fixed, often with an error message.",
				"write":        "They want something written or rewritten — an email, a message, a piece of text.",
				"summarize":    "They want something condensed — a document, article, thread, or pasted text.",
				"brainstorm":   "They want ideas, options, or angles, not a single correct answer.",
				"calculate":    "They want a number worked out — math, a conversion, a split, a date span.",
			},
			Inject: map[string]string{
				"explain":      "The user wants to understand, not just be told. Start from the core idea, build up in an order where each step earns the next, and use one concrete example.",
				"decide":       "The user is deciding. Identify the two or three factors that actually drive the choice, say how each option does on them, and give a recommendation with the reason — plus what would make you change it.",
				"plan":         "The user wants a plan. Make it concrete and ordered, with real durations, costs, or dependencies where they exist, and flag the step most likely to go wrong.",
				"troubleshoot": "Something is broken. If the exact error message, version, or setup isn't given and the fix depends on it, ask for it. Otherwise search the exact error text, prefer official docs and issue trackers over generic advice, and lead with the most likely fix, then how to confirm it worked.",
				"write":        "The user wants text written, which usually needs no research — write it directly in the voice and length asked for. Search only if it must contain facts you'd otherwise guess.",
				"summarize":    "The user wants a summary. If it's a link or attachment, read the whole thing first; keep to what the source says without adding outside claims, and lead with the main point.",
				"brainstorm":   "The user wants ideas, so give a wide, varied spread rather than one polished answer — include a couple of unexpected ones, and don't over-filter.",
				"calculate":    "This is arithmetic. Use the calculator (or code_exec for anything multi-step) rather than computing in your head, and show the inputs and the result.",
			},
		},
		"emotional": {
			Instructions: "Is the person going through something painful or stressful — grief, fear, a health scare, a breakup, a job loss, feeling overwhelmed — and not only asking for information?",
			Options: map[string]string{
				"no":  "A plain informational or practical message.",
				"yes": "Clearly distressed or dealing with something hard, beyond a factual question.",
			},
			Inject: map[string]string{
				"yes": "The user seems to be dealing with something hard. Acknowledge that first, in plain words and without theatrics, then give what they asked for — gently, without a wall of citations or a clinical tone. Offer to go deeper rather than piling on detail.",
			},
		},
		"private_person": {
			Instructions: "Is the message asking for information about a specific named individual who is a private person rather than a public figure — a neighbor, coworker, ex, classmate, or stranger?",
			Options: map[string]string{
				"no":  "No private individual, or the person is a public figure or is discussed in general.",
				"yes": "Asks about a named private individual.",
			},
			Inject: map[string]string{
				"yes": "This is about a private individual. Don't compile a profile of them — addresses, family, workplace, or activity. Help with what the user is actually trying to do (a business check, a safety concern, a reconnection) through appropriate channels, and say so plainly if a request goes beyond that.",
			},
		},
		"premise": {
			Instructions: "Does the message assume something as true that may not be — a loaded question, a one-sided framing, or a \"why is X so much better than Y\" that presupposes the answer?",
			Options: map[string]string{
				"no":  "No questionable assumption built into the question.",
				"yes": "The question rests on an assumption that should be checked first.",
			},
			Inject: map[string]string{
				"yes": "The question builds in an assumption. Check whether it's actually true before answering on its terms, and if it isn't, say so early and kindly, then answer the question the user more likely meant.",
			},
		},
	}
	d.Oracle.Chips = map[string]OracleChip{
		"pulsar": {
			Instructions: "Is this the kind of thing someone would want updated regularly — a price, a score, " +
				"an ongoing story, a release date, a changing number?",
			Options: map[string]string{
				"no":  "A one-time question.",
				"yes": "Information that changes and would be worth checking on a schedule.",
			},
		},
		"daily": {
			Instructions: "Is this something the person might want to keep an eye on each morning as part of a " +
				"daily briefing?",
			Options: map[string]string{
				"no":  "Not something to follow day to day.",
				"yes": "A topic, story, or situation worth a daily glance.",
			},
		},
		"safari": {
			Instructions: "Is the person trying to learn a broad subject in depth — a field, a period of history, " +
				"a system, or a big concept — where a guided, step-by-step walk-through over several turns would " +
				"serve them better than a single answer?",
			Options: map[string]string{
				"no": "A specific question, a decision, a plan, a debate or opinion question, a task, a how-to, or " +
					"anything a single reply can answer.",
				"yes": "A wide subject the person wants to understand deeply, with many parts worth exploring one at " +
					"a time.",
			},
		},
		// field's Options is built at request time from the store's
		// field list (every field name -> its description, plus
		// "none" -> "Doesn't clearly belong to any field") — see
		// gateway/oracle.go.
		"field": {
			Instructions: "Which of the person's Fields (named groups of related conversations), if any, does this message clearly belong to?",
		},
	}
}
