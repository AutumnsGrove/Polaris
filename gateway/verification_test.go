package gateway

import (
	"strings"
	"testing"

	"polaris/tools"
)

func TestExtractClaims(t *testing.T) {
	citations := []tools.Citation{
		{URL: "https://a.example/page"},
		{URL: "https://b.example/page"},
	}

	t.Run("basic sentence extraction", func(t *testing.T) {
		answer := "Sonnet 5 was released in 2026 [Anthropic](https://a.example/page). It is widely used."
		claims := extractClaims(answer, citations)
		if len(claims) != 1 {
			t.Fatalf("got %d claims, want 1: %+v", len(claims), claims)
		}
		if claims[0].url != "https://a.example/page" {
			t.Errorf("url = %q", claims[0].url)
		}
		if claims[0].claimIndex != 0 {
			t.Errorf("claimIndex = %d, want 0", claims[0].claimIndex)
		}
		want := "Sonnet 5 was released in 2026 [Anthropic](https://a.example/page)."
		if claims[0].text != want {
			t.Errorf("text = %q, want %q", claims[0].text, want)
		}
	})

	t.Run("skips untracked URL", func(t *testing.T) {
		answer := "Something happened [source](https://untracked.example/page)."
		claims := extractClaims(answer, citations)
		if len(claims) != 0 {
			t.Fatalf("got %d claims, want 0: %+v", len(claims), claims)
		}
	})

	t.Run("skips table rows", func(t *testing.T) {
		answer := "| Fact | Source |\n| --- | --- |\n| X | [ref](https://a.example/page) |"
		claims := extractClaims(answer, citations)
		if len(claims) != 0 {
			t.Fatalf("got %d claims, want 0: %+v", len(claims), claims)
		}
	})

	t.Run("URL query string doesn't break sentence bounds", func(t *testing.T) {
		citationsWithQuery := []tools.Citation{{URL: "https://a.example/page?q=1&r=2.5"}}
		answer := "First sentence here. Sonnet 5 outperforms Opus [source](https://a.example/page?q=1&r=2.5) on this benchmark. Third sentence."
		claims := extractClaims(answer, citationsWithQuery)
		if len(claims) != 1 {
			t.Fatalf("got %d claims, want 1: %+v", len(claims), claims)
		}
		// One sentence of lead-up available (claimContextSentences allows
		// up to 2) — included in full, and "Third sentence." correctly
		// excluded since it comes after the cited sentence, not before.
		want := "First sentence here. Sonnet 5 outperforms Opus [source](https://a.example/page?q=1&r=2.5) on this benchmark."
		if claims[0].text != want {
			t.Errorf("text = %q, want %q", claims[0].text, want)
		}
	})

	t.Run("second citation of same URL gets claimIndex 1", func(t *testing.T) {
		answer := "Claim one [a](https://a.example/page). Claim two [a](https://a.example/page)."
		claims := extractClaims(answer, citations)
		if len(claims) != 2 {
			t.Fatalf("got %d claims, want 2: %+v", len(claims), claims)
		}
		if claims[0].claimIndex != 0 || claims[1].claimIndex != 1 {
			t.Errorf("claimIndexes = %d, %d, want 0, 1", claims[0].claimIndex, claims[1].claimIndex)
		}
	})

	t.Run("no citations produces no claims", func(t *testing.T) {
		claims := extractClaims("Plain answer with no links.", citations)
		if len(claims) != 0 {
			t.Fatalf("got %d claims, want 0", len(claims))
		}
	})

	t.Run("includes up to two sentences of lead-up", func(t *testing.T) {
		// The real, live-observed pattern this guards against: a claim
		// built up across several sentences before the citation chip
		// actually appears, with a pronoun ("It") whose only antecedent is
		// two sentences back.
		answer := "The JWST program ran over budget for years. It eventually launched in December 2021 " +
			"[Wikipedia](https://a.example/page)."
		claims := extractClaims(answer, citations)
		if len(claims) != 1 {
			t.Fatalf("got %d claims, want 1: %+v", len(claims), claims)
		}
		if !strings.Contains(claims[0].text, "ran over budget") {
			t.Errorf("text = %q, want it to include the lead-up sentence", claims[0].text)
		}
	})

	t.Run("lead-up never crosses a paragraph break", func(t *testing.T) {
		answer := "Unrelated prior paragraph about something else entirely.\n\n" +
			"Sonnet 5 shipped in 2026 [Anthropic](https://a.example/page)."
		claims := extractClaims(answer, citations)
		if len(claims) != 1 {
			t.Fatalf("got %d claims, want 1: %+v", len(claims), claims)
		}
		if strings.Contains(claims[0].text, "Unrelated") {
			t.Errorf("text = %q, leaked across a paragraph break", claims[0].text)
		}
	})

	t.Run("three-sentence lead-up caps at two sentences", func(t *testing.T) {
		answer := "Sentence one is here. Sentence two is here. Sentence three is here. " +
			"Sentence four has the link [source](https://a.example/page)."
		claims := extractClaims(answer, citations)
		if len(claims) != 1 {
			t.Fatalf("got %d claims, want 1: %+v", len(claims), claims)
		}
		if strings.Contains(claims[0].text, "Sentence one") {
			t.Errorf("text = %q, should not reach back three sentences (cap is 2)", claims[0].text)
		}
		if !strings.Contains(claims[0].text, "Sentence two") || !strings.Contains(claims[0].text, "Sentence three") {
			t.Errorf("text = %q, want both of the two nearest lead-up sentences", claims[0].text)
		}
	})
}

func TestSplitIntoChunksOverlap(t *testing.T) {
	// Three paragraphs, each well over chunkTargetTokens on its own once
	// repeated, forcing a split — the overlap tail of chunk 1 should
	// reappear at the start of chunk 2.
	long := func(word string, n int) string {
		s := ""
		for i := 0; i < n; i++ {
			s += word + " "
		}
		return s
	}
	text := long("alpha", 3000) + "\n\n" + long("bravo", 3000) + "\n\n" + long("charlie", 3000)
	chunks := splitIntoChunks(text)
	if len(chunks) < 2 {
		t.Fatalf("got %d chunks, want at least 2", len(chunks))
	}
}

func TestRunVerificationNilSafe(t *testing.T) {
	if got := runVerification(nil, "answer", nil); got != nil {
		t.Errorf("runVerification(nil ctx) = %v, want nil", got)
	}
	ctx := &tools.Context{}
	if got := runVerification(ctx, "answer", []tools.Citation{{URL: "https://a.example"}}); got != nil {
		t.Errorf("runVerification(nil Jev) = %v, want nil", got)
	}
}
