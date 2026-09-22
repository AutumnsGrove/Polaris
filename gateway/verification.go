package gateway

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"unicode"

	"polaris/jev"
	"polaris/tools"
)

// jevPerTurnCapUSD/jevMonthlyCapUSD mirror tools/compare_sources.go's own
// (unexported, package-private) constants of the same value — kept as a
// separate copy here rather than exported from tools, since the two
// features track entirely independent running totals (compare_sources
// checks mid-turn via ctx.JevSpentThisTurn while a tool call is still live;
// this checks post-turn, in a detached goroutine, against the exact same
// tools.Context — the two calls' spend still lands in the same running
// total either way, so a turn that both compares sources and verifies
// citations shares one $0.01 budget across both, not two separate ones).
const (
	jevPerTurnCapUSD = 0.01
	jevMonthlyCapUSD = 5.00
)

// verificationConfidenceThreshold gates the one visible mark this feature
// ever shows — see docs/plans/source-verification-badge.md's UI section:
// every "easy" case in live testing hit confidence 1.0, and the one
// genuinely-ambiguous case landed at 0.35, so 0.85 comfortably excludes
// ambiguous/compound claims while passing clean matches. A missed badge
// costs nothing; a wrongly-shown one costs trust, so this stays a
// deliberately conservative default until tuned from real usage.
const verificationConfidenceThreshold = 0.85

// verificationCharsPerToken is a conservative chars-per-token estimate for
// budgeting evidence size before ever calling Jev — the live-measured
// pass/fail boundary used repetitive filler text (~7 chars/token), denser
// than real English (~4 chars/token); rounding down to 3.5 leaves headroom
// rather than reusing that filler-text measurement directly.
const verificationCharsPerToken = 3.5

// verificationTokenBudget targets ~24-26k tokens, not Jev's full 32k
// context window, to leave headroom for the fan-out questions' own
// instructions/criteria overhead (measured live: ~1.3k tokens added by 8
// questions' worth of instructions on top of the source text).
const verificationTokenBudget = 25000

const verificationCharBudget = int(verificationTokenBudget * verificationCharsPerToken)

// chunkTargetTokens/chunkOverlapTokens: paragraph-boundary chunk splits for
// evidence over budget — see the plan doc's "Chunking design" section.
const (
	chunkTargetTokens  = 7000
	chunkOverlapTokens = 300
)

// maxChunksPerSource bounds how many chunk calls one source can generate —
// a long page (a full Wikipedia article, a long doc-site page) could
// otherwise split into dozens of chunks; only the ones most lexically
// relevant to the pending claims are actually sent to Jev, since most
// chunks of a long page legitimately have nothing to do with any specific
// claim. Deliberately adjustable, not a tightly-reasoned figure.
const maxChunksPerSource = 5

// claim is one claim/source pair extracted from the answer's own markdown —
// see extractClaims.
type claim struct {
	url        string
	claimIndex int // 0-based occurrence of url among this answer's own citation links, in document order
	text       string
}

// citationLinkRe matches a markdown link `[text](url)`, optionally followed
// by a space-separated `"title"` — mirrors citations.ts's own matching
// rule closely enough for claim extraction's purposes (tracked-URL links
// only; the URL group stops at the first `)` or whitespace, so a title
// attribute after the URL doesn't get swallowed into it).
var citationLinkRe = regexp.MustCompile(`\[([^\]]+)\]\(([^)\s]+)(?:\s+"[^"]*")?\)`)

// claimContextSentences is how many sentences before the one carrying the
// citation link also get included in the claim text sent to Jev — see
// widenedClaimStart. 2, not the bare enclosing sentence alone: a real
// answer often builds up a claim across several sentences before the
// citation chip actually appears (e.g. "X happened. It was caused by Y.
// [source](url)."), and a claim missing that lead-up loses the antecedent
// for any pronoun/back-reference in its own sentence — confirmed live-
// relevant, not a theoretical concern. Jev's real per-call cost is small
// enough ($0.00004-0.0002/call measured live) that this costs effectively
// nothing against the token budget even on a chatty multi-sentence answer.
const claimContextSentences = 2

// extractClaims walks answer's raw markdown for [text](url) links whose URL
// is one of citations' tracked URLs, and takes the enclosing sentence plus
// claimContextSentences of lead-up as that link's claim — mirroring
// citations.ts's renderInlineCitations matching rule (tracked-URL links
// only) done here in Go, since claim extraction needs the raw markdown,
// not rendered/sanitized HTML. claimIndex tracks each URL's own occurrence
// order so the frontend can later mark the specific chip a claim came
// from, not every chip citing that URL — one source can back several
// claims with different verdicts.
func extractClaims(answer string, citations []tools.Citation) []claim {
	tracked := make(map[string]bool, len(citations))
	for _, c := range citations {
		tracked[c.URL] = true
	}

	matches := citationLinkRe.FindAllStringSubmatchIndex(answer, -1)
	var claims []claim
	nextIndex := map[string]int{}
	for _, m := range matches {
		matchStart, matchEnd := m[0], m[1]
		urlStart, urlEnd := m[4], m[5]
		url := answer[urlStart:urlEnd]
		if !tracked[url] {
			continue
		}
		if inTableRow(answer, matchStart) {
			continue
		}
		sentStart, sentEnd := enclosingSentence(answer, matchStart, matchEnd)
		claimStart := widenedClaimStart(answer, sentStart, claimContextSentences)
		text := strings.TrimSpace(answer[claimStart:sentEnd])
		if text == "" {
			continue
		}
		idx := nextIndex[url]
		nextIndex[url] = idx + 1
		claims = append(claims, claim{url: url, claimIndex: idx, text: text})
	}
	return claims
}

// widenedClaimStart scans backward from sentStart (the start of the
// sentence that actually carries the citation link) across up to
// extraSentences more sentence boundaries, stopping early at a paragraph
// break or the start of the text — so the claim sent to Jev includes
// however much lead-up actually precedes the cited sentence, without
// reaching into an unrelated prior paragraph.
func widenedClaimStart(text string, sentStart int, extraSentences int) int {
	pos := sentStart
	for i := 0; i < extraSentences; i++ {
		// Only skip spaces/tabs here, never '\n' — a newline anywhere in
		// this gap means pos sits right after a line or paragraph break,
		// which should stop the widen regardless of how many newlines
		// there are. An earlier version skipped over '\n' as if it were
		// ordinary whitespace, which walked straight through a "\n\n"
		// paragraph break and pulled in the previous paragraph's own
		// last sentence — caught by a live test asserting exactly this.
		j := pos - 1
		for j >= 0 && (text[j] == ' ' || text[j] == '\t') {
			j--
		}
		if j < 0 {
			break // start of text — nothing earlier to include
		}
		if text[j] == '\n' {
			break // line/paragraph break right before pos — stop here
		}
		// j now sits on the previous sentence's own terminal punctuation
		// (or plain text, if it never got one) — scan back past it to
		// find where that sentence itself starts.
		newPos := 0
		for k := j - 1; k >= 0; k-- {
			if k > 0 && text[k] == '\n' && text[k-1] == '\n' {
				newPos = k + 1
				break
			}
			if isSentenceEnd(text, k) {
				newPos = k + 1
				break
			}
		}
		for newPos < len(text) && (text[newPos] == ' ' || text[newPos] == '\n' || text[newPos] == '\t') {
			newPos++
		}
		if newPos >= pos {
			break // safety: didn't actually move backward
		}
		pos = newPos
	}
	return pos
}

// inTableRow reports whether pos falls on a markdown table row (a line
// starting with "|") — verifying a table cell's fragment against its
// source is a much weaker signal than a real sentence, so claim extraction
// skips it entirely rather than checking a claim that's really just a
// table label.
func inTableRow(text string, pos int) bool {
	lineStart := strings.LastIndexByte(text[:pos], '\n') + 1
	lineEnd := len(text)
	if i := strings.IndexByte(text[pos:], '\n'); i >= 0 {
		lineEnd = pos + i
	}
	return strings.HasPrefix(strings.TrimSpace(text[lineStart:lineEnd]), "|")
}

// enclosingSentence returns the bounds of the sentence containing
// text[start:end], scanning outward to the nearest sentence-ending
// punctuation (or paragraph break) on each side. Scans only outside
// [start,end] itself, so a URL's own "." or "?" (e.g. a query string)
// inside the link being matched never gets mistaken for a sentence
// boundary.
func enclosingSentence(text string, start, end int) (int, int) {
	sentStart := 0
	for i := start - 1; i >= 0; i-- {
		if i > 0 && text[i] == '\n' && text[i-1] == '\n' {
			sentStart = i + 1
			break
		}
		if isSentenceEnd(text, i) {
			sentStart = i + 1
			break
		}
	}
	for sentStart < len(text) && (text[sentStart] == ' ' || text[sentStart] == '\n' || text[sentStart] == '\t') {
		sentStart++
	}

	sentEnd := len(text)
	for i := end; i < len(text); i++ {
		if isSentenceEnd(text, i) {
			sentEnd = i + 1
			break
		}
		if i+1 < len(text) && text[i] == '\n' && text[i+1] == '\n' {
			sentEnd = i
			break
		}
	}
	return sentStart, sentEnd
}

func isSentenceEnd(text string, i int) bool {
	c := text[i]
	if c != '.' && c != '!' && c != '?' {
		return false
	}
	j := i + 1
	return j >= len(text) || text[j] == ' ' || text[j] == '\n' || text[j] == '\t'
}

// ClaimVerification is one claim's full result — every claim considered,
// not just the ones that cleared the confidence threshold. runVerification
// always returns the full set; turn.go filters down to Supported-only
// (via filterSupportedMarks) for the real badge/persistence path, and
// hands the unfiltered set to AskRequest.WaitVerification's debug/
// stress-testing response instead (see ask.go) so a test caller can see
// exactly what Jev said — confidence, choice, even a claim that errored or
// had no evidence to check against — not just the filtered subset a real
// chat turn shows as a badge.
type ClaimVerification struct {
	URL        string `json:"url"`
	ClaimIndex int    `json:"claim_index"`
	ClaimText  string `json:"claim_text"`
	// Choice/Confidence are "" / 0 when this claim was never actually
	// checked — no stored evidence for its URL (web_search-only citation,
	// or an empty/scanned-PDF extraction), every chunk call failed, or a
	// budget cap was hit before its source's turn came up. Reason
	// explains which when that happens; empty when a real Jev answer
	// came back.
	Choice     string  `json:"choice"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason,omitempty"`
	// Supported is true iff this claim cleared the confidence threshold
	// with Choice == "supported" — the exact gate the real badge/
	// persistence path uses (see filterSupportedMarks).
	Supported bool `json:"supported"`
}

// runVerification checks each of answer's own inline citations against the
// text actually fetched for them this turn (agentCtx.EvidenceForURL), using
// Jev — see docs/plans/source-verification-badge.md. Returns every claim's
// full result, unfiltered; see filterSupportedMarks for the real badge's
// "supported, at/above threshold only" view. Nil whenever there's nothing
// to check at all: no Jev client configured, no answer, or no tracked
// citations (as opposed to a claim that was extracted but couldn't be
// checked, which still gets its own ClaimVerification with an empty Choice
// and a Reason, not a silent omission).
func runVerification(agentCtx *tools.Context, answer string, citations []tools.Citation) []ClaimVerification {
	if agentCtx == nil || agentCtx.Jev == nil || answer == "" || len(citations) == 0 {
		return nil
	}
	claims := extractClaims(answer, citations)
	if len(claims) == 0 {
		return nil
	}

	byURL := map[string][]claim{}
	for _, c := range claims {
		byURL[c.url] = append(byURL[c.url], c)
	}

	var mu sync.Mutex
	var results []ClaimVerification
	var wg sync.WaitGroup
	for url, urlClaims := range byURL {
		evidence, ok := agentCtx.EvidenceForURL(url)
		if !ok || strings.TrimSpace(evidence) == "" {
			// web_search-only citation never actually web_read, or a
			// scanned/empty-extraction PDF page — skip the call entirely
			// rather than spend on what would only ever come back
			// not_addressed anyway. Still reported, just with no
			// Choice/Confidence, so a debug caller can see why nothing
			// happened instead of the claim silently vanishing.
			mu.Lock()
			for _, c := range urlClaims {
				results = append(results, ClaimVerification{URL: c.url, ClaimIndex: c.claimIndex, ClaimText: c.text, Reason: "no stored evidence for this URL"})
			}
			mu.Unlock()
			continue
		}
		wg.Add(1)
		go func(url string, urlClaims []claim, evidence string) {
			defer wg.Done()
			// This runs inside a goroutine already one level detached from
			// handleTurn's own panic recovery (see runVerification's
			// caller in turn.go) — guard each source's own goroutine too,
			// so one source's panic can't take the others down with it.
			defer func() {
				if r := recover(); r != nil {
					log.Error("panic verifying source", "url", url, "panic", r)
				}
			}()
			sourceResults := verifySource(agentCtx, url, evidence, urlClaims)
			mu.Lock()
			results = append(results, sourceResults...)
			mu.Unlock()
		}(url, urlClaims, evidence)
	}
	wg.Wait()
	return results
}

// filterSupportedMarks reduces runVerification's full per-claim results
// down to the real badge's "supported, at/above threshold only" view — see
// the plan doc's UI section. Only ever returns "supported" marks — never a
// "contradicted" warning, even though Jev itself distinguishes them —
// because a false "your source disagrees" is editorially worse than a
// missed badge; a contradicted-mark v2 needs real usage data first, not a
// launch-day guess.
func filterSupportedMarks(results []ClaimVerification) []VerificationMark {
	var marks []VerificationMark
	for _, r := range results {
		if r.Supported {
			marks = append(marks, VerificationMark{URL: r.URL, ClaimIndex: r.ClaimIndex, Choice: r.Choice, Confidence: r.Confidence})
		}
	}
	return marks
}

// verifySource runs one source's pending claims through Jev — a single
// call if the evidence fits the token budget, otherwise one call per
// selected chunk, all of that source's claims as parallel Choice questions
// either way (never one call per claim) — and reduces each claim's result
// across however many chunk calls it took. See the plan doc's "Chunking
// design" and "Reduce per-claim across chunk results" sections.
func verifySource(agentCtx *tools.Context, url, evidence string, claims []claim) []ClaimVerification {
	questions := make(map[string]jev.ChoiceQuestion, len(claims))
	overheadChars := 0
	criteria := map[string]string{
		"supported":           "The source confirms this claim",
		"partially_supported": "The source partially confirms this",
		"contradicted":        "The source contradicts this claim",
		"not_addressed":       "The source does not address this at all",
	}
	for i, c := range claims {
		key := fmt.Sprintf("c%d", i)
		instructions := fmt.Sprintf("Does the source support the claim: %s", c.text)
		questions[key] = jev.ChoiceQuestion{Instructions: instructions, Criteria: criteria}
		overheadChars += len(instructions)
		for _, v := range criteria {
			overheadChars += len(v)
		}
	}

	var chunks []string
	if len(evidence)+overheadChars <= verificationCharBudget {
		chunks = []string{evidence}
	} else {
		chunks = selectRelevantChunks(evidence, claims, maxChunksPerSource)
	}

	perClaim := make(map[string][]jev.ChoiceAnswer)
	for _, chunk := range chunks {
		if agentCtx.JevSpentThisTurn() >= jevPerTurnCapUSD {
			log.Warn("verification: per-turn Jev budget already used, stopping", "url", url)
			break
		}
		if agentCtx.JevCostThisMonth != nil {
			if used, err := agentCtx.JevCostThisMonth(); err != nil {
				log.Warn("verification: checking jev monthly cost failed, proceeding anyway", "err", err)
			} else if used >= jevMonthlyCapUSD {
				log.Warn("verification: monthly Jev budget reached, stopping", "url", url)
				break
			}
		}

		// context.Background(), not agentCtx.Ctx — agentCtx.Ctx is
		// cancelled the instant handleTurn returns (see ws.go's `defer
		// cancel()` right after the call that spawns this whole detached
		// goroutine tree), which is already true by the time this runs.
		// generateSuggestions makes the exact same choice for the exact
		// same reason.
		resp, err := agentCtx.Jev.AskChoice(context.Background(), chunk, questions)
		if err != nil {
			log.Warn("verification: jev call failed", "url", url, "err", err)
			continue
		}
		agentCtx.AddJevCost(resp.Usage.CostUSD)
		if agentCtx.LogJevCost != nil {
			// Logged immediately, before reduction below — an audit trail
			// even if a later chunk call in this same source fails
			// partway through.
			if err := agentCtx.LogJevCost(resp.Usage.CostUSD); err != nil {
				log.Warn("verification: logging jev cost failed", "err", err)
			}
		}
		for key, ans := range resp.Answers {
			perClaim[key] = append(perClaim[key], ans)
		}
	}

	results := make([]ClaimVerification, len(claims))
	for i, c := range claims {
		key := fmt.Sprintf("c%d", i)
		answers := perClaim[key]
		results[i] = ClaimVerification{URL: url, ClaimIndex: c.claimIndex, ClaimText: c.text}
		if len(answers) == 0 {
			results[i].Reason = "no successful jev call (budget cap hit or every call failed)"
			continue
		}

		var hasSupported, hasContradicted bool
		var bestSupportedConf, bestOverallConf float64
		var bestOverallChoice string
		for _, r := range answers {
			if r.Confidence > bestOverallConf {
				bestOverallConf, bestOverallChoice = r.Confidence, r.Choice
			}
			if r.Choice == "supported" && r.Confidence >= verificationConfidenceThreshold {
				hasSupported = true
				if r.Confidence > bestSupportedConf {
					bestSupportedConf = r.Confidence
				}
			}
			if r.Choice == "contradicted" && r.Confidence >= verificationConfidenceThreshold {
				hasContradicted = true
			}
		}
		// Two chunks disagreeing at high confidence (one supported, one
		// contradicted) shows no badge at all rather than guessing which
		// chunk wins — see the plan doc's reduce-across-chunks rule. Still
		// reported (Choice/Confidence from the highest-confidence answer
		// across chunks) for the debug path — Supported stays false either
		// way, so the real badge path is unaffected.
		if hasSupported && !hasContradicted {
			results[i].Choice, results[i].Confidence, results[i].Supported = "supported", bestSupportedConf, true
		} else {
			results[i].Choice, results[i].Confidence = bestOverallChoice, bestOverallConf
			if hasSupported && hasContradicted {
				results[i].Reason = "chunks disagreed at high confidence"
			}
		}
	}
	return results
}

// selectRelevantChunks splits evidence on paragraph boundaries and returns
// at most max chunks, the ones most lexically relevant to claims — word
// overlap, not embeddings (tools.Context.Embed is nil on a real fraction of
// deployments; see the plan doc's "Chunk selection" section). Chunks are
// returned in original document order, not score order, so overlap
// boundaries between adjacent selected chunks still read sensibly.
func selectRelevantChunks(evidence string, claims []claim, max int) []string {
	chunks := splitIntoChunks(evidence)
	if len(chunks) <= max {
		return chunks
	}

	var claimText strings.Builder
	for _, c := range claims {
		claimText.WriteString(c.text)
		claimText.WriteString(" ")
	}
	wanted := wordSet(claimText.String())

	type scored struct {
		text  string
		score int
		order int
	}
	scoredChunks := make([]scored, len(chunks))
	for i, chunk := range chunks {
		scoredChunks[i] = scored{text: chunk, score: overlapScore(wordSet(chunk), wanted), order: i}
	}
	sort.SliceStable(scoredChunks, func(i, j int) bool { return scoredChunks[i].score > scoredChunks[j].score })
	if len(scoredChunks) > max {
		scoredChunks = scoredChunks[:max]
	}
	sort.SliceStable(scoredChunks, func(i, j int) bool { return scoredChunks[i].order < scoredChunks[j].order })

	out := make([]string, len(scoredChunks))
	for i, sc := range scoredChunks {
		out[i] = sc.text
	}
	return out
}

// splitIntoChunks splits text on paragraph breaks into chunks of roughly
// chunkTargetTokens each, with chunkOverlapTokens of trailing context
// carried into the start of the next chunk so a claim's supporting text
// straddling a chunk boundary isn't silently split away from itself.
func splitIntoChunks(text string) []string {
	paragraphs := strings.Split(text, "\n\n")
	targetChars := int(chunkTargetTokens * verificationCharsPerToken)
	overlapChars := int(chunkOverlapTokens * verificationCharsPerToken)

	var chunks []string
	var cur strings.Builder
	for _, p := range paragraphs {
		if cur.Len() > 0 && cur.Len()+len(p) > targetChars {
			chunks = append(chunks, cur.String())
			prev := cur.String()
			overlapStart := len(prev) - overlapChars
			if overlapStart < 0 {
				overlapStart = 0
			}
			cur.Reset()
			cur.WriteString(prev[overlapStart:])
		}
		if cur.Len() > 0 {
			cur.WriteString("\n\n")
		}
		cur.WriteString(p)
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// wordSet lowercases s and returns the set of its words longer than 3
// characters — short words (articles, prepositions) overlap between any
// two unrelated texts and would otherwise dilute the relevance score.
func wordSet(s string) map[string]bool {
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	set := make(map[string]bool, len(words))
	for _, w := range words {
		if len(w) > 3 {
			set[w] = true
		}
	}
	return set
}

func overlapScore(a, b map[string]bool) int {
	score := 0
	for w := range a {
		if b[w] {
			score++
		}
	}
	return score
}
