// embeddings.go implements `polaris embeddings` — status, backfill, and
// reindex for the semantic index over chat history and stars (see
// docs/plans/embeddings.md).
//
// Thin client, like every other command that talks to a running Polaris:
// the real config.yaml and polaris.db live inside the container's named
// volume, not on the host this binary runs on, so the only correct route is
// the container's own REST API (cmd/docker_client.go).
package cmd

import (
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

var embeddingsKind string
var embeddingsLimit int
var embeddingsYes bool

var embeddingsCmd = &cobra.Command{
	Use:   "embeddings",
	Short: "Inspect and rebuild the semantic index over chats and stars",
	Long: `Inspect and rebuild the semantic index used for semantic search over your
own chat history and Constellation stars.

The index is built automatically in small batches as you use Polaris; these
commands are for the two cases that need a human: loading an index for
history that predates enabling embeddings, and rebuilding it after changing
embeddings.model in config.yaml.

  polaris embeddings status
  polaris embeddings backfill [--kind message|star|all] [--limit N]
  polaris embeddings reindex  [--kind message|star|all] [--yes]

Changing the embedding model makes every stored vector incomparable, so
semantic search turns itself off (keyword search keeps working) until a
reindex finishes. That's deliberate — the alternative is a silent API spend
and a load spike at startup.`,
}

var embeddingsStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show index coverage, the configured model, and embedding spend to date",
	Args:  cobra.NoArgs,
	RunE:  runEmbeddingsStatus,
}

var embeddingsBackfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "Embed every eligible chat message and star that isn't indexed yet",
	Long: `Embed every eligible row that isn't indexed yet.

Resumable for free: what's left to do is derived from what's already stored,
so an interrupted run (or a run that hit a provider error) can simply be
re-run.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEmbeddingsJob("backfill")
	},
}

var embeddingsReindexCmd = &cobra.Command{
	Use:   "reindex",
	Short: "Delete every stored vector and rebuild the index from scratch",
	Long: `Delete every stored vector and rebuild the index from scratch.

Use this after changing embeddings.model — vectors from different models
aren't comparable, so they're kept per-model and semantic search is off
until they've all been recomputed.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if !embeddingsYes {
			return fmt.Errorf("reindex deletes every stored vector before rebuilding — re-run with --yes to confirm")
		}
		return runEmbeddingsJob("reindex")
	},
}

func init() {
	for _, c := range []*cobra.Command{embeddingsBackfillCmd, embeddingsReindexCmd} {
		c.Flags().StringVar(&embeddingsKind, "kind", "all", "which rows to process: message, star, or all")
		c.Flags().IntVar(&embeddingsLimit, "limit", 0, "process at most N rows this run (0 = everything outstanding)")
	}
	embeddingsReindexCmd.Flags().BoolVarP(&embeddingsYes, "yes", "y", false, "confirm the destructive delete step")
	embeddingsCmd.AddCommand(embeddingsStatusCmd, embeddingsBackfillCmd, embeddingsReindexCmd)
	rootCmd.AddCommand(embeddingsCmd)
}

// EmbeddingStatusResponse mirrors gateway.EmbeddingStatusResponse. Redefined
// here rather than imported so the CLI has no build-time dependency on the
// gateway package's wire types, same as cmd/stats.go's own shape.
type EmbeddingStatusResponse struct {
	Configured bool                         `json:"configured"`
	Model      string                       `json:"model,omitempty"`
	Index      map[string]EmbeddingCoverage `json:"index,omitempty"`
	CostUSD    float64                      `json:"cost_usd"`
}

type EmbeddingCoverage struct {
	Indexed    int `json:"indexed"`
	Eligible   int `json:"eligible"`
	StaleModel int `json:"stale_model"`
}

func runEmbeddingsStatus(cmd *cobra.Command, args []string) error {
	url := dockerLocalBaseURL() + "/api/embeddings/status"
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("fetching embedding status failed (status %d)", resp.StatusCode)
	}

	var body EmbeddingStatusResponse
	if err := readCappedJSON(resp, &body); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}

	if !body.Configured {
		fmt.Println("embeddings are not configured — set embeddings.provider in config.yaml")
		fmt.Println("(or ollama.base_url for the local option); see SETUP.md")
		return nil
	}

	fmt.Printf("model: %s\n", body.Model)
	// Sorted so the output is stable across runs rather than however the
	// JSON map happened to iterate.
	kinds := make([]string, 0, len(body.Index))
	for kind := range body.Index {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		cov := body.Index[kind]
		pct := 100.0
		if cov.Eligible > 0 {
			pct = float64(cov.Indexed) / float64(cov.Eligible) * 100
		}
		fmt.Printf("%-8s %d / %d eligible indexed (%.0f%%)\n", kind+":", cov.Indexed, cov.Eligible, pct)
		if cov.StaleModel > 0 {
			fmt.Printf("         %d row(s) are from a different model — run `polaris embeddings reindex --yes`\n", cov.StaleModel)
		}
	}
	fmt.Printf("embedding spend to date: $%.4f\n", body.CostUSD)
	return nil
}

func runEmbeddingsJob(verb string) error {
	if embeddingsKind != "" && embeddingsKind != "all" && embeddingsKind != "message" && embeddingsKind != "star" {
		return fmt.Errorf("--kind must be message, star, or all")
	}
	url := fmt.Sprintf("%s/api/embeddings/%s?kind=%s&limit=%d",
		dockerLocalBaseURL(), verb, embeddingsKind, embeddingsLimit)

	// Generous: measured at 80 seconds for a real 2,344-message history
	// against a hosted model, and a local model on an SBC is far slower. The
	// server has its own 30-minute ceiling and reports a real error if it
	// hits it, so this only needs to outlast that.
	client := &http.Client{Timeout: 45 * time.Minute}

	if verb == "reindex" {
		fmt.Println("deleting every stored vector, then rebuilding via the running container — this may take a while...")
	} else {
		fmt.Println("embedding outstanding rows via the running container — this may take a while...")
	}

	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		// The server distinguishes the two 409s in its body (not configured vs
		// a run already in flight); surface that instead of guessing, falling
		// back to naming both only when the body is empty.
		reason := readCappedText(resp)
		if reason == "no details" {
			reason = "either embeddings aren't configured, or another backfill is already running"
		}
		return fmt.Errorf("the server refused the run: %s", reason)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s failed (status %d): %s", verb, resp.StatusCode, readCappedText(resp))
	}

	var body struct {
		Messages int     `json:"messages"`
		Stars    int     `json:"stars"`
		CostUSD  float64 `json:"cost_usd"`
	}
	if err := readCappedJSON(resp, &body); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}

	fmt.Printf("done — %d message(s), %d star(s), $%.6f\n", body.Messages, body.Stars, body.CostUSD)
	if body.Messages == 0 && body.Stars == 0 {
		fmt.Println("(nothing was outstanding — already up to date)")
	}
	return nil
}

// readCappedText reads a small error body for reporting; capped so a huge
// or malicious response can't blow up the CLI's memory.
func readCappedText(resp *http.Response) string {
	var body struct {
		Error string `json:"error"`
	}
	if err := readCappedJSON(resp, &body); err == nil && body.Error != "" {
		return body.Error
	}
	return "no details"
}
