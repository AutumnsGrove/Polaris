//go:build livecheck

package tools

// This file is the "does looksLikePaywall fire at all in the real world"
// sanity check called for by docs/plans/hill-climbing-tier1.md's item #2 and
// its suggested-order step 1. It hits real, live paywalled pages over the
// network, so it's gated behind the `livecheck` build tag and never runs as
// part of `go test ./...` or CI — run it explicitly:
//
//	go test -tags livecheck ./tools/ -run TestPaywallLiveCheck -v
//
// It also archives each fetched page's raw HTML into
// dev/fixtures/paywall_livecheck/ (gitignored, same tree as
// dev/fixtures_export's output) so the pages don't need re-fetching for the
// hand-labelling step the plan calls for next.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"polaris/tavily"
)

type livecheckCase struct {
	slug   string
	outlet string
	url    string
	// want is a human expectation, not an assertion — the whole point of
	// this check is to find out whether reality matches it.
	want string
}

var livecheckCases = []livecheckCase{
	{"bloomberg-1", "Bloomberg", "https://www.bloomberg.com/news/articles/2026-09-24/stock-market-today-dow-s-p-live-updates", "paywalled"},
	{"bloomberg-2", "Bloomberg", "https://www.bloomberg.com/news/articles/2026-09-24/goldman-sachs-is-underweight-hyperscalers-on-debt-supply-surge", "paywalled"},
	{"bloomberg-3", "Bloomberg", "https://www.bloomberg.com/news/articles/2026-09-16/fed-decision-inflation-war-and-ai-feed-into-bond-market-s-toxic-stew", "paywalled"},
	{"medium-1", "Medium", "https://shivanivishnoi11.medium.com/the-one-promise-for-2026-2d3f9708e237", "paywalled"},
	{"medium-2", "Medium", "https://fabemitchell.medium.com/how-to-survive-2026-d51a25f4008b", "paywalled"},
	{"medium-3", "Medium", "https://medium.com/no-time/gpt-6-is-coming-in-july-2026-and-everything-will-change-3ace06689417", "paywalled"},
	// Clean, always-free baselines — real running prose recovered by a
	// direct fetch (no fallback chain), for contrast against the
	// chrome-passthrough cases above when building a detector.
	{"wikipedia-1", "Wikipedia", "https://en.wikipedia.org/wiki/Bloomberg_L.P.", "free"},
	{"verge-1", "The Verge", "https://www.theverge.com/", "free"},
}

// tavilyClient is built once from TAVILY_API_KEY so the fallback-chain leg
// (Wayback -> Tavily, mirroring handleWebRead's own order in web_read.go) is
// exercised for real rather than just fetchAndExtract in isolation — the
// first livecheck run found fetchAndExtract 403ing on every case, which
// means looksLikePaywall never even runs in production for these URLs;
// what actually determines whether web_read recovers is this chain.
var tavilyClient = tavily.NewClient(os.Getenv("TAVILY_API_KEY"))

func TestPaywallLiveCheck(t *testing.T) {
	if tavilyClient == nil {
		t.Log("TAVILY_API_KEY not set — Tavily fallback leg will be skipped, only fetchAndExtract/Wayback are exercised")
	}

	outDir := filepath.Join("..", "dev", "fixtures", "paywall_livecheck")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", outDir, err)
	}

	for _, c := range livecheckCases {
		c := c
		t.Run(c.slug, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// Save the raw HTML independently of fetchAndExtract (which only
			// returns already-extracted text) so it's available later as
			// item #1/#2's hand-labelled corpus, same shape as
			// dev/fixtures_export's pages/*.html.
			if raw, err := fetchRaw(ctx, c.url); err != nil {
				t.Logf("raw fetch failed: %v", err)
			} else {
				htmlPath := filepath.Join(outDir, c.slug+".html")
				if err := os.WriteFile(htmlPath, raw, 0o644); err != nil {
					t.Logf("writing %s: %v", htmlPath, err)
				}
			}

			stage := "direct"
			_, _, _, text, _, err := fetchAndExtract(ctx, c.url, nil, 0)

			// Mirrors handleWebRead's own fallback order (web_read.go:169-206):
			// archive.org first (free), Tavily only if that didn't resolve it.
			if err != nil || looksLikePaywall(text) {
				if _, _, _, wbText, _, wbErr := fetchFromWayback(ctx, c.url, nil, 0); wbErr == nil {
					stage = "wayback"
					text, err = wbText, nil
				} else {
					t.Logf("%s: wayback fallback failed: %v", c.outlet, wbErr)
				}
			}

			if tavilyClient != nil && (err != nil || looksLikePaywall(text) || looksEmpty(text)) {
				if tavilyText, tErr := tavilyClient.Extract(ctx, c.url, true); tErr == nil && !looksEmpty(tavilyText) {
					stage = "tavily"
					// Mirrors handleWebRead's own post-processing of Tavily's
					// result (web_read.go) — exercises the real fix, not just
					// the raw fallback text.
					text, err = stripBoilerplateLines(tavilyText), nil
				} else {
					t.Logf("%s: tavily fallback failed or still empty: %v", c.outlet, tErr)
				}
			}

			if err != nil {
				t.Logf("%s (%s): UNRECOVERED after full fallback chain: %v", c.outlet, c.url, err)
				return
			}

			paywall := looksLikePaywall(text)
			empty := looksEmpty(text)
			snippet := text
			if len(snippet) > 200 {
				snippet = snippet[:200]
			}
			snippet = strings.ReplaceAll(snippet, "\n", " ")

			t.Logf("%s | want=%s | recovered_via=%s | chars=%d | looksLikePaywall=%v | looksEmpty=%v | snippet=%q",
				c.outlet, c.want, stage, len(text), paywall, empty, snippet)

			// Full recovered text, for building/tuning a chrome-vs-article
			// detector by hand rather than guessing from a 200-char snippet.
			txtPath := filepath.Join(outDir, c.slug+"."+stage+".txt")
			if err := os.WriteFile(txtPath, []byte(text), 0o644); err != nil {
				t.Logf("writing %s: %v", txtPath, err)
			}
		})
	}
}

func fetchRaw(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Polaris/1.0; +https://github.com/AutumnsGrove/Polaris)")
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 20<<20))
}
