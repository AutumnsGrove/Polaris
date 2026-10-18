// comet.go implements `polaris comet classify` and `polaris comet
// generate` — thin HTTP clients against the running container's own REST
// API, same pattern as `polaris constellation backfill`
// (cmd/constellation_backfill.go). Comet runs inside the long-lived server
// process (scheduler or HTTP handler); the CLI binary outside the container
// never touches polaris.db directly.
package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"polaris/gateway"
)

var (
	cometClassifyLimit  int
	cometClassifyPeriod string
	cometGeneratePeriod string
	cometBackfillFrom   string
	cometBackfillTo     string
)

var cometCmd = &cobra.Command{
	Use:   "comet",
	Short: "Comet retrospective admin commands",
}

var cometClassifyCmd = &cobra.Command{
	Use:   "classify",
	Short: "Run Comet's Approach classifier over a period's eligible threads",
	Args:  cobra.NoArgs,
	RunE:  runCometClassify,
}

var cometGenerateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Assemble (or re-assemble) a Comet edition for a period",
	Args:  cobra.NoArgs,
	RunE:  runCometGenerate,
}

var cometBackfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "Classify and generate Comet editions for every month in a range",
	Args:  cobra.NoArgs,
	RunE:  runCometBackfill,
}

func init() {
	cometClassifyCmd.Flags().IntVarP(&cometClassifyLimit, "n", "n", 0, "process only the first N eligible threads (oldest thread first), instead of the full period — useful for testing before committing to a full run")
	cometClassifyCmd.Flags().StringVar(&cometClassifyPeriod, "period", "", "period to classify, \"YYYY-MM\" (default: the most recent complete month)")
	cometGenerateCmd.Flags().StringVar(&cometGeneratePeriod, "period", "", "period to generate, \"YYYY-MM\" (default: the most recent complete month)")
	cometBackfillCmd.Flags().StringVar(&cometBackfillFrom, "from", "", "first period to backfill, \"YYYY-MM\" (default: the month of the earliest message in the database)")
	cometBackfillCmd.Flags().StringVar(&cometBackfillTo, "to", "", "last period to backfill, \"YYYY-MM\" (default: the most recent complete month)")
	cometCmd.AddCommand(cometClassifyCmd)
	cometCmd.AddCommand(cometGenerateCmd)
	cometCmd.AddCommand(cometBackfillCmd)
	rootCmd.AddCommand(cometCmd)
}

// cometAPIError turns a non-success response from the Comet endpoints into an
// error. Those handlers answer with http.Error (a plain-text body), not JSON,
// so the body is read as text — decoding it as {"error": ...} silently threw
// away the server's actual reason (e.g. "a comet run is already in progress")
// and left only a bare status code.
func cometAPIError(resp *http.Response, what string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	if msg := strings.TrimSpace(string(body)); msg != "" {
		return fmt.Errorf("%s failed (status %d): %s", what, resp.StatusCode, msg)
	}
	return fmt.Errorf("%s failed (status %d)", what, resp.StatusCode)
}

// cometURL builds a Comet endpoint URL, query-escaping each non-empty param
// so a stray "&" in a --period/--from/--to value can't inject another one.
func cometURL(path string, params ...string) string {
	q := url.Values{}
	for i := 0; i+1 < len(params); i += 2 {
		if params[i+1] != "" {
			q.Set(params[i], params[i+1])
		}
	}
	u := dockerLocalBaseURL() + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	return u
}

func runCometClassify(cmd *cobra.Command, args []string) error {
	url := cometURL("/api/comet/classify", "limit", fmt.Sprint(cometClassifyLimit), "period", cometClassifyPeriod)
	// A full month's classify run can be a hundred-plus real LLM calls at
	// ~5 concurrent — same generous-timeout reasoning as the Constellation
	// backfill command.
	client := &http.Client{Timeout: 2 * time.Hour}

	if cometClassifyLimit > 0 {
		fmt.Printf("classifying up to %d thread(s) via the running container...\n", cometClassifyLimit)
	} else {
		fmt.Println("classifying every eligible thread for the period via the running container — this may take a while...")
	}

	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return cometAPIError(resp, "classify")
	}

	var body struct {
		Processed int    `json:"processed"`
		Period    string `json:"period"`
	}
	if err := readCappedJSON(resp, &body); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}
	fmt.Printf("done — classified %d thread(s) for %s\n", body.Processed, body.Period)
	return nil
}

func runCometBackfill(cmd *cobra.Command, args []string) error {
	url := cometURL("/api/comet/backfill", "from", cometBackfillFrom, "to", cometBackfillTo)
	client := &http.Client{Timeout: 4 * time.Hour}

	fmt.Println("backfilling Comet month by month via the running container — this may take a long while...")
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return cometAPIError(resp, "backfill")
	}
	var body struct {
		Results []gateway.CometBackfillResult `json:"results"`
	}
	if err := readCappedJSON(resp, &body); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}
	for _, r := range body.Results {
		status := "ok"
		if r.Skipped {
			status = "no conversations, skipped"
		} else if r.Error != "" {
			status = "FAILED: " + r.Error
		} else if !r.Generated {
			status = "classified, not generated"
		}
		fmt.Printf("  %s — %d trail(s), %s\n", r.Period, r.Trails, status)
	}
	fmt.Printf("done — %d month(s)\n", len(body.Results))
	return nil
}

func runCometGenerate(cmd *cobra.Command, args []string) error {
	url := cometURL("/api/comet/generate", "period", cometGeneratePeriod)
	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return cometAPIError(resp, "generate")
	}
	fmt.Println("generation started — watch the container logs or reload /comet in a moment")
	return nil
}
