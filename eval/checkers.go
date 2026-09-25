package eval

import (
	"regexp"
	"strconv"
	"strings"
)

// titleQuotePrefix/answerLikeTitle mirror gateway/turn.go's own unexported
// regexes of the same name exactly — this package can't import gateway
// (gateway imports half the app, eval must not become one of its
// dependents), so the check is duplicated rather than shared. Keep these
// in sync with gateway/turn.go by hand if either changes; a case here
// exists precisely to catch that kind of drift in the actual generated
// title, not in this checker's own logic.
var (
	titleQuotePrefix = regexp.MustCompile(`^["'“‘]+|["'”’]+$`)
	answerLikeTitle  = regexp.MustCompile(`(?i)^(yes|no|sure|actually|unfortunately|correct|indeed|according to)\b[\s,.:!—-]`)
)

// CheckTitleFormat applies the format half of hill-climbing.md's Tier 2
// title check — word count and "doesn't read like an answer." raw is the
// model's own completion, unsanitized (sanitizeGeneratedTitle in
// gateway/turn.go already strips quotes/trailing punctuation before a
// title is ever stored — checking the raw output here is deliberately
// stricter, since a title that only looks right after cleanup still cost
// a wasted generation if the model routinely gets the shape wrong).
func CheckTitleFormat(raw string) (ok bool, reason string) {
	title := strings.TrimSpace(raw)
	title = strings.TrimSpace(titleQuotePrefix.ReplaceAllString(title, ""))
	if title == "" {
		return false, "empty title"
	}
	words := strings.Fields(title)
	if len(words) < 2 || len(words) > 8 {
		return false, "word count outside 2-8 (want ~3-6): " + title
	}
	if answerLikeTitle.MatchString(title) {
		return false, "reads like an answer, not a title: " + title
	}
	if strings.HasSuffix(title, ".") || strings.HasSuffix(title, "!") {
		return false, "trailing punctuation: " + title
	}
	return true, ""
}

// suggestionListPrefix mirrors gateway/turn.go's own regex of the same
// name — see CheckTitleFormat's doc comment on why this is duplicated
// rather than shared.
var suggestionListPrefix = regexp.MustCompile(`^(?:[-*•]\s+|\d+[.)]\s+)`)

// CheckSuggestionsFormat applies hill-climbing.md's Tier 2 suggestions
// check: exactly 3 lines, each ending in "?" once list-style prefixes are
// stripped the same way production parsing does.
func CheckSuggestionsFormat(raw string) (ok bool, reason string) {
	var lines []string
	for _, line := range strings.Split(raw, "\n") {
		line = suggestionListPrefix.ReplaceAllString(strings.TrimSpace(line), "")
		line = strings.Trim(line, "\"")
		if line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) != 3 {
		return false, "want exactly 3 non-empty lines, got " + strconv.Itoa(len(lines))
	}
	for _, line := range lines {
		if !strings.HasSuffix(line, "?") {
			return false, "line doesn't end in '?': " + line
		}
	}
	return true, ""
}
