// Package main is a measurement tool for Prism's Oracle `ui` check
// (docs/plans/intelligent-ui.md, "Verification" -> "Oracle spike"): how often
// does it nudge toward a visual block when it shouldn't, and miss one when it
// should? The number that matters is the FALSE-POSITIVE rate, because an
// unneeded block is worse than a missed one.
//
// It drives the real gateway.RunOracle with the shipped prompts.yaml and the
// shipped (or config.yaml's) thresholds, so what it measures is what runs in
// production, including how the ui question behaves among its ~20 siblings.
// It records the raw ui probabilities once, then scores them offline at both
// dial thresholds (Normal = the check's threshold, Low = threshold + offset),
// so retuning a threshold needs no new Jev spend.
//
// Usage (from the repo root, where prompts.yaml lives):
//
//	go run ./dev/ui_spike                                  # hand-written corpus only
//	go run ./dev/ui_spike -real /tmp/openers.json          # plus unlabeled real messages
//
// The -real file is a JSON array of {"message": "..."}; it is for eyeballing
// what would fire on real traffic, so it carries no expected label.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"polaris/config"
	"polaris/gateway"
	"polaris/jev"
	"polaris/models"
)

type item struct {
	Message string `json:"message"`
	Expect  string `json:"expect,omitempty"` // a block name (compare, steps, ...) | none | borderline | "" (unlabeled real)
}

type scored struct {
	item
	Winner string             `json:"winner"`
	Probs  map[string]float64 `json:"probs"`
	Cost   float64            `json:"cost_usd"`
	OK     bool               `json:"ok"` // false: Jev timed out / errored / dropped the answer
}

func load(path string) []item {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("reading %s: %v", path, err)
	}
	var items []item
	if err := json.Unmarshal(raw, &items); err != nil {
		log.Fatalf("parsing %s: %v", path, err)
	}
	return items
}

// decide is the check's own rule: a non-"none" winner that clears the bar.
func decide(s scored, threshold float64) string {
	if s.Winner == "" || s.Winner == "none" || s.Probs[s.Winner] < threshold {
		return "none"
	}
	return s.Winner
}

func main() {
	corpusPath := flag.String("corpus", "dev/ui_spike/corpus.json", "labeled corpus")
	realPath := flag.String("real", "", "optional unlabeled real messages, JSON [{message}]")
	outPath := flag.String("out", "ui_spike_results.jsonl", "raw per-message results")
	cfgPath := flag.String("config", "config.yaml", "config.yaml (API key only; thresholds come from the shipped defaults)")
	conc := flag.Int("conc", 4, "concurrent Oracle calls")
	retries := flag.Int("retries", 2, "re-asks when Jev times out (it does ~14% of the time)")
	flag.Parse()

	items := load(*corpusPath)
	if *realPath != "" {
		items = append(items, load(*realPath)...)
	}

	cfg, err := config.Load(*cfgPath, models.Registry)
	if err != nil {
		log.Fatalf("loading config: %v", err)
	}
	client := jev.NewClient(cfg.OpenRouter.BaseURL, cfg.OpenRouter.APIKey)
	if client == nil {
		log.Fatal("no OpenRouter API key configured")
	}
	// The shipped policy, deliberately not cfg.Oracle: this measures the
	// defaults a fresh install runs, regardless of local config.yaml tuning.
	rules := config.DefaultOracle()
	uiRule := rules.Checks["ui"]
	normalBar, lowBar := uiRule.Threshold, uiRule.Threshold+uiRule.VisualsLowOffset

	results := make([]scored, len(items))
	var wg sync.WaitGroup
	sem := make(chan struct{}, *conc)
	for i := range items {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			s := scored{item: items[i]}
			for attempt := 0; attempt <= *retries && !s.OK; attempt++ {
				res := gateway.RunOracle(context.Background(), client, gateway.OracleInput{
					CurrentMessage: items[i].Message,
					IsFirstMessage: true,
					Visuals:        "normal", // ask the question; the bars are applied below
					Rules:          rules,
				})
				s.Cost += res.CostUSD
				for _, c := range res.Checks {
					if c.Key == "ui" {
						s.Winner, s.Probs, s.OK = c.Winner, c.Probabilities, true
					}
				}
			}
			results[i] = s
		}(i)
	}
	wg.Wait()

	out, err := os.Create(*outPath)
	if err != nil {
		log.Fatalf("creating %s: %v", *outPath, err)
	}
	enc := json.NewEncoder(out)
	var cost float64
	failed := 0
	for _, s := range results {
		_ = enc.Encode(s)
		cost += s.Cost
		if !s.OK {
			failed++
		}
	}
	out.Close()

	fmt.Printf("%d messages, total Jev cost $%.4f, %d with no ui answer even after retries\n", len(results), cost, failed)
	fmt.Printf("bars: Normal %.2f, Low %.2f (shipped defaults)\n\n", normalBar, lowBar)

	for _, dial := range []struct {
		name string
		bar  float64
	}{{"Normal", normalBar}, {"Low", lowBar}} {
		report(dial.name, dial.bar, results)
	}
	fmt.Printf("raw results: %s\n", *outPath)
}

func report(name string, bar float64, results []scored) {
	var (
		noneTotal, falsePos         int
		blockTotal, hit, wrongBlock int
	)
	var fps, misses, wrongs, borderline, real []string

	for _, s := range results {
		if !s.OK {
			continue
		}
		got := decide(s, bar)
		line := fmt.Sprintf("  %-9s %.2f  %s", s.Winner, s.Probs[s.Winner], truncate(s.Message, 78))
		switch s.Expect {
		case "none":
			noneTotal++
			if got != "none" {
				falsePos++
				fps = append(fps, line)
			}
		case "borderline":
			borderline = append(borderline, fmt.Sprintf("  fires=%-8s %-9s %.2f  %s", got, s.Winner, s.Probs[s.Winner], truncate(s.Message, 60)))
		case "":
			if got != "none" {
				real = append(real, line)
			}
		default: // any other label is the block this message should get
			blockTotal++
			switch {
			case got == s.Expect:
				hit++
			case got == "none":
				misses = append(misses, fmt.Sprintf("  want %-7s got %-9s %.2f  %s", s.Expect, s.Winner, s.Probs[s.Winner], truncate(s.Message, 60)))
			default:
				wrongBlock++
				wrongs = append(wrongs, fmt.Sprintf("  want %-7s got %-9s %.2f  %s", s.Expect, got, s.Probs[got], truncate(s.Message, 60)))
			}
		}
	}

	fmt.Printf("=== %s dial (bar %.2f) ===\n", name, bar)
	fmt.Printf("false positives: %d / %d none-messages (%.1f%%)   <- the number that matters\n", falsePos, noneTotal, pct(falsePos, noneTotal))
	fmt.Printf("block hit rate:  %d / %d (%.1f%%), wrong block %d, missed %d\n", hit, blockTotal, pct(hit, blockTotal), wrongBlock, len(misses))
	section("false positives (a block nobody needed)", fps)
	section("wrong block", wrongs)
	section("missed", misses)
	section("borderline (not scored)", borderline)
	section("unlabeled real messages that would fire", real)
	fmt.Println()
}

func section(title string, lines []string) {
	if len(lines) == 0 {
		return
	}
	sort.Strings(lines)
	fmt.Printf("%s:\n%s\n", title, strings.Join(lines, "\n"))
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func truncate(s string, n int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}
