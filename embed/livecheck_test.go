//go:build livecheck

package embed

// Live check for the hosted provider — the "does it actually behave like
// this against the real API, today" step that docs/plans/embeddings.md's
// int8 storage decision rests on. It spends real money (fractions of a
// cent) and needs a real key, so it is gated behind the `livecheck` build
// tag and never runs as part of `go test ./...` or CI:
//
//	OPENROUTER_API_KEY=sk-or-... go test -tags livecheck ./embed/ -run TestOpenRouterLiveCheck -v
//
// The assertions here are the four properties the design depends on, each
// of which would silently break storage or ranking if it changed:
//
//  1. 1024 dimensions — the stored width, and what makes the int8 blob
//     1KB/vector.
//  2. Determinism — identical text must produce an identical vector, or
//     content_hash stops being a valid cache key and every re-index
//     rewrites rows it didn't need to.
//  3. int8 representability — every component an exact multiple of 1/128,
//     which is what makes the int8 encoding lossless for this model.
//  4. Reported cost — the settings panel's Embeddings row shows the
//     provider's own number, so it must actually be reported.

import (
	"context"
	"math"
	"os"
	"testing"
)

func TestOpenRouterLiveCheck(t *testing.T) {
	key := os.Getenv("OPENROUTER_API_KEY")
	if key == "" {
		t.Skip("OPENROUTER_API_KEY not set")
	}

	client := NewOpenRouter(key, "", "")
	if client == nil {
		t.Fatal("NewOpenRouter returned nil with a key set")
	}
	ctx := context.Background()

	const text = "the potato deployment runs Polaris in Docker on a Le Potato SBC"
	res, err := client.EmbedBatch(ctx, []string{text, "roasting potatoes at 400F", "an unrelated sentence about jazz"})
	if err != nil {
		t.Fatalf("live embed failed: %v", err)
	}
	t.Logf("model=%s dims=%d cost=$%.8f tokens=%d", res.Model, res.Dims, res.CostUSD, res.PromptTokens)

	if res.Dims != 1024 {
		t.Errorf("dims = %d, want 1024", res.Dims)
	}
	if len(res.Vectors) != 3 {
		t.Fatalf("got %d vectors, want 3", len(res.Vectors))
	}
	if res.CostUSD <= 0 {
		t.Errorf("CostUSD = %v, want the provider's reported (non-zero) cost", res.CostUSD)
	}
	if res.PromptTokens <= 0 {
		t.Errorf("PromptTokens = %d, want non-zero", res.PromptTokens)
	}

	// (2) determinism, batch-vs-single.
	again, err := client.Embed(ctx, text)
	if err != nil {
		t.Fatalf("second live embed failed: %v", err)
	}
	if sim := CosineSimilarity(res.Vectors[0], again); sim != 1 {
		t.Errorf("repeat call cosine = %v, want exactly 1.0 (determinism is what makes content_hash a valid cache key)", sim)
	}

	// (3) every component an exact multiple of 1/128.
	for i, x := range res.Vectors[0] {
		scaled := float64(x) * int8Scale
		if math.Abs(scaled-math.Round(scaled)) > 1e-6 {
			t.Fatalf("component %d = %v is not a multiple of 1/%d — int8 storage would be lossy for this model", i, x, int8Scale)
		}
	}
	decoded := DecodeInt8(EncodeInt8(res.Vectors[0]))
	for i := range res.Vectors[0] {
		if decoded[i] != res.Vectors[0][i] {
			t.Fatalf("int8 round-trip changed component %d: %v -> %v", i, res.Vectors[0][i], decoded[i])
		}
	}

	// Sanity: the potato sentence should sit closer to itself than to the
	// jazz sentence. Not a ranking-quality claim, just "the vectors mean
	// something".
	if CosineSimilarity(res.Vectors[0], res.Vectors[2]) >= CosineSimilarity(res.Vectors[0], res.Vectors[1]) {
		t.Log("note: the two potato sentences did not out-similar the jazz one — interesting but not a failure")
	}
}
