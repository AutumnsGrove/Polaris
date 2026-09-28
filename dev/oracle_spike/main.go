// Package main is a throwaway spike for validating Oracle mode's draft Jev
// checks (docs/plans/oracle-mode.md) against real, short conversational
// prompts before any of it ships behind a toggle — every previous live Jev
// spike (docs/plans/source-verification.md) was claim-vs-source
// verification over documents, never a 6-word question, so this needs its
// own live check. Not wired into cmd/ (this is a one-off measurement tool,
// not a feature), following dev/fakeopenrouter's convention of a standalone
// dev/*/main.go with its own package.
//
// Usage:
//
//	go run ./dev/oracle_spike -in /tmp/oracle_spike_final.json -out oracle_spike_results.jsonl
//
// Input is a JSON array of {id, thread_id, message, prev_user_message,
// is_first_in_thread} — see the plan's Milestone A for how that sample was
// pulled from the potato's real thread history. One AskChoice call per
// prompt, every check fired in parallel in that one call (mirrors the real
// design), with clarify only included when is_first_in_thread, matching
// v1's "clarify only runs on a thread's first message" rule. Output is
// JSONL, one line per prompt, for hand-grading.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"polaris/config"
	"polaris/jev"
	"polaris/models"
)

type spikePrompt struct {
	ID              int64  `json:"id"`
	ThreadID        string `json:"thread_id"`
	Message         string `json:"message"`
	PrevUserMessage string `json:"prev_user_message"`
	IsFirstInThread bool   `json:"is_first_in_thread"`
}

type spikeResult struct {
	ID         int64                       `json:"id"`
	Message    string                      `json:"message"`
	LatencyMs  int64                       `json:"latency_ms"`
	CostUSD    float64                     `json:"cost_usd"`
	Answers    map[string]jev.ChoiceAnswer `json:"answers"`
	ErrMessage string                      `json:"err_message,omitempty"`
}

// questionPreamble mirrors the draft prompts.yaml's oracle.question_preamble.
const questionPreamble = "You are reading a message someone sent to a research assistant that searches the web and cites sources. Answer about the latest message; an earlier message, if present, is only context."

func checks() map[string]jev.ChoiceQuestion {
	return map[string]jev.ChoiceQuestion{
		"focus": {
			Instructions: questionPreamble + " Which answering style best fits this message? Pick \"off\" unless one style is clearly a better fit than a normal, balanced answer.",
			Criteria: map[string]string{
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
			Instructions: questionPreamble + " Does answering this well require searching the web or reading current information?",
			Criteria: map[string]string{
				"yes": "Needs current facts, specifics, prices, news, or anything that could have changed recently.",
				"no":  "Casual conversation, writing help, brainstorming, or stable general knowledge.",
			},
		},
		"high_stakes": {
			Instructions: questionPreamble + " Would acting on a wrong answer to this message risk someone's health, legal standing, money, or physical safety?",
			Criteria: map[string]string{
				"none":      "Low stakes; a wrong answer would be an inconvenience at most.",
				"medical":   "Health, symptoms, medications, dosages, diagnoses, or treatment.",
				"legal":     "Laws, rights, contracts, disputes, taxes as a legal matter, or legal procedure.",
				"financial": "Investing, debt, taxes, insurance, large purchases, or other money decisions.",
				"safety":    "Physical danger — electrical, chemical, structural, vehicles, weapons, outdoor risks.",
			},
		},
		"intent": {
			Instructions: questionPreamble + " What kind of thing is this message mainly asking about?",
			Criteria: map[string]string{
				"general":    "None of the other options clearly fits.",
				"place":      "A place, business, restaurant, or something nearby or at a specific location.",
				"book":       "A book, author, or what to read.",
				"film_tv":    "A movie, TV show, actor, or what to watch.",
				"music":      "A song, album, artist, or what to listen to.",
				"product":    "A specific product or buying decision.",
				"weather":    "Weather or a forecast.",
				"video":      "A specific YouTube video or its contents.",
				"code":       "A code repository, library, or programming project.",
				"definition": "The meaning, pronunciation, or origin of a word.",
			},
		},
		"recall": {
			Instructions: questionPreamble + " Does this message refer back to an earlier conversation the person had with the assistant (for example \"like we talked about\", \"that thing from last week\", \"remember when\")?",
			Criteria: map[string]string{
				"no":  "No reference to a past conversation.",
				"yes": "Explicitly or clearly refers to something discussed before.",
			},
		},
	}
}

const clarifyInstructions = "Is this message ambiguous enough that the answer would be substantially different depending on something the person didn't say — so that asking one question first would clearly save wasted research?"

func main() {
	inPath := flag.String("in", "", "path to input JSON array of spike prompts (required)")
	outPath := flag.String("out", "oracle_spike_results.jsonl", "path to write JSONL results")
	cfgPath := flag.String("config", "config.yaml", "path to config.yaml (used only for the OpenRouter/Jev API key)")
	flag.Parse()

	if *inPath == "" {
		log.Fatal("-in is required")
	}

	raw, err := os.ReadFile(*inPath)
	if err != nil {
		log.Fatalf("reading %s: %v", *inPath, err)
	}
	var prompts []spikePrompt
	if err := json.Unmarshal(raw, &prompts); err != nil {
		log.Fatalf("parsing %s: %v", *inPath, err)
	}

	cfg, err := config.Load(*cfgPath, models.Registry)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	jevClient := jev.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey)
	if jevClient == nil {
		log.Fatal("no OpenRouter API key configured — jev.NewClient returned nil")
	}

	outFile, err := os.Create(*outPath)
	if err != nil {
		log.Fatalf("creating %s: %v", *outPath, err)
	}
	defer outFile.Close()
	enc := json.NewEncoder(outFile)

	var totalCost float64
	var latencies []int64

	for i, p := range prompts {
		questions := checks()
		if p.IsFirstInThread {
			questions["clarify"] = jev.ChoiceQuestion{
				Instructions: questionPreamble + " " + clarifyInstructions,
				Criteria: map[string]string{
					"no":  "Clear enough to answer well, or any ambiguity has an obvious default.",
					"yes": "Two or more very different readings, or a missing detail that changes everything.",
				},
			}
		}

		var state string
		if p.PrevUserMessage != "" {
			state = fmt.Sprintf("Previous message: %s\n\nLatest message: %s", p.PrevUserMessage, p.Message)
		} else {
			state = fmt.Sprintf("Latest message: %s", p.Message)
		}

		start := time.Now()
		resp, err := jevClient.AskChoice(context.Background(), state, questions)
		latency := time.Since(start).Milliseconds()

		result := spikeResult{ID: p.ID, Message: p.Message, LatencyMs: latency}
		if err != nil {
			result.ErrMessage = err.Error()
			fmt.Printf("[%d/%d] id=%d ERROR: %v\n", i+1, len(prompts), p.ID, err)
		} else {
			result.Answers = resp.Answers
			result.CostUSD = resp.Usage.CostUSD
			totalCost += resp.Usage.CostUSD
			latencies = append(latencies, latency)
			fmt.Printf("[%d/%d] id=%d latency=%dms cost=$%.5f focus=%s research=%s high_stakes=%s\n",
				i+1, len(prompts), p.ID, latency, resp.Usage.CostUSD,
				resp.Answers["focus"].Choice, resp.Answers["research"].Choice, resp.Answers["high_stakes"].Choice)
		}
		if err := enc.Encode(result); err != nil {
			log.Fatalf("writing result for id=%d: %v", p.ID, err)
		}
	}

	sortInt64(latencies)
	fmt.Printf("\n%d prompts, total cost $%.4f\n", len(prompts), totalCost)
	if len(latencies) > 0 {
		fmt.Printf("latency p50=%dms p95=%dms max=%dms\n",
			latencies[len(latencies)*50/100], latencies[len(latencies)*95/100], latencies[len(latencies)-1])
	}
	fmt.Printf("results written to: %s\n", *outPath)
}

func sortInt64(s []int64) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
