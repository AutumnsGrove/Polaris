// LiveNewsBench support (github.com/YunfanZhang42/LiveNewsBench,
// arXiv:2602.13543). Its README describes a question/answer/link/
// event_date schema, but that's wrong for the actual published
// datasets/*/human_verified_test.jsonl files — confirmed by downloading
// and inspecting real rows rather than trusting the docs (see
// HANDOFF.md). The real per-line JSON object has: question, answer,
// canary, category, date, articles ([]{title, article_content} — full
// article text inline, not a URL), plus grading-pipeline metadata
// (human_review_status, meets_criteria, qa_created_by, ...) this harness
// doesn't need. There is no source URL/domain to exclude from the
// agent's own search the way the README implied — the dataset simply
// doesn't carry one — so unlike the originally planned design, no
// domain-exclusion hook is needed in BuildQuery.
//
// Grading reuses SimpleQA's grader verbatim — LiveNewsBench's own
// README states "we adopted the same grading prompt as SimpleQA" and
// its evals/grade_simple_qa_prompt.txt is a byte-for-byte copy — so
// BuildSimpleQAGraderPrompt/GradeSimpleQA in simpleqa.go serve both
// suites; only the dataset loader and query differ.
package benchmark

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// liveNewsBenchRow is the real (not README-described) shape of one line
// in datasets/*/human_verified_test.jsonl. Fields this harness doesn't
// use (articles, human_review_status, meets_criteria, ...) are omitted
// rather than modeled, since they're not needed to run or grade against
// the dataset.
type liveNewsBenchRow struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
	Category string `json:"category"`
	Date     string `json:"date"`
	Canary   string `json:"canary"`
}

// liveNewsBenchMaxLineBytes bounds bufio.Scanner's token buffer. Real
// rows run up to ~230KB (full article bodies inline in the `articles`
// field) — bufio.Scanner's 64KB default token limit silently errors on
// anything larger, so this must be set explicitly rather than relying
// on the default. Set well above the largest observed row for headroom
// against future releases with more/longer source articles.
const liveNewsBenchMaxLineBytes = 8 << 20 // 8MB

// LoadLiveNewsBenchDataset reads a LiveNewsBench release's JSONL file
// (e.g. human_verified_test.jsonl — the README's recommended default,
// per-row human-reviewed rather than auto-generated-only). Category and
// Canary are carried into Row.ProblemTopic/Row.Canary as informational
// fields; Canary is NOT used for decryption here the way BrowseComp uses
// it — LiveNewsBench's questions/answers are already plaintext.
func LoadLiveNewsBenchDataset(path string) ([]Row, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("opening dataset: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), liveNewsBenchMaxLineBytes)

	var rows []Row
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r liveNewsBenchRow
		if err := json.Unmarshal(line, &r); err != nil {
			return nil, fmt.Errorf("parsing row %d: %w", len(rows)+1, err)
		}
		if r.Question == "" || r.Answer == "" {
			return nil, fmt.Errorf("row %d missing question or answer", len(rows)+1)
		}
		rows = append(rows, Row{
			Index:        len(rows),
			Problem:      r.Question,
			Answer:       r.Answer,
			ProblemTopic: r.Category,
			Canary:       r.Canary,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading dataset: %w", err)
	}
	return rows, nil
}
