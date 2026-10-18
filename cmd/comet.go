// comet.go implements `polaris comet classify` and `polaris comet
// generate` — thin HTTP clients against the running container's own REST
// API, same pattern as `polaris constellation backfill`
// (cmd/constellation_backfill.go). Comet runs inside the long-lived server
// process (scheduler or HTTP handler); the CLI binary outside the container
// never touches polaris.db directly.
package cmd

import (
	"fmt"
	"net/http"
	"time"

	"github.com/spf13/cobra"
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
	cometClassifyCmd.Flags().IntVarP(&cometClassifyLimit, "n", "n", 0, "process only the N most recently active eligible threads, instead of the full period — useful for testing before committing to a full run")
	cometClassifyCmd.Flags().StringVar(&cometClassifyPeriod, "period", "", "period to classify, \"YYYY-MM\" (default: the most recent complete month)")
	cometGenerateCmd.Flags().StringVar(&cometGeneratePeriod, "period", "", "period to generate, \"YYYY-MM\" (default: the most recent complete month)")
	cometBackfillCmd.Flags().StringVar(&cometBackfillFrom, "from", "", "first period to backfill, \"YYYY-MM\" (default: the month of the earliest message in the database)")
	cometBackfillCmd.Flags().StringVar(&cometBackfillTo, "to", "", "last period to backfill, \"YYYY-MM\" (default: the most recent complete month)")
	cometCmd.AddCommand(cometClassifyCmd)
	cometCmd.AddCommand(cometGenerateCmd)
	cometCmd.AddCommand(cometBackfillCmd)
	rootCmd.AddCommand(cometCmd)
}

func runCometClassify(cmd *cobra.Command, args []string) error {
	url := fmt.Sprintf("%s/api/comet/classify?limit=%d", dockerLocalBaseURL(), cometClassifyLimit)
	if cometClassifyPeriod != "" {
		url += "&period=" + cometClassifyPeriod
	}
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
		var errBody struct {
			Error string `json:"error"`
		}
		_ = readCappedJSON(resp, &errBody)
		if errBody.Error != "" {
			return fmt.Errorf("%s", errBody.Error)
		}
		return fmt.Errorf("classify failed (status %d)", resp.StatusCode)
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
	url := fmt.Sprintf("%s/api/comet/backfill", dockerLocalBaseURL())
	params := ""
	if cometBackfillFrom != "" {
		params += "from=" + cometBackfillFrom
	}
	if cometBackfillTo != "" {
		if params != "" {
			params += "&"
		}
		params += "to=" + cometBackfillTo
	}
	if params != "" {
		url += "?" + params
	}
	client := &http.Client{Timeout: 4 * time.Hour}

	fmt.Println("backfilling Comet month by month via the running container — this may take a long while...")
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = readCappedJSON(resp, &errBody)
		if errBody.Error != "" {
			return fmt.Errorf("%s", errBody.Error)
		}
		return fmt.Errorf("backfill failed (status %d)", resp.StatusCode)
	}
	var body struct {
		Results []struct {
			Period    string `json:"period"`
			Trails    int    `json:"trails"`
			Generated bool   `json:"generated"`
			Error     string `json:"error"`
		} `json:"results"`
	}
	if err := readCappedJSON(resp, &body); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}
	for _, r := range body.Results {
		status := "ok"
		if r.Error != "" {
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
	url := fmt.Sprintf("%s/api/comet/generate", dockerLocalBaseURL())
	if cometGeneratePeriod != "" {
		url += "?period=" + cometGeneratePeriod
	}
	client := &http.Client{Timeout: 30 * time.Second}

	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		var errBody struct {
			Error string `json:"error"`
		}
		_ = readCappedJSON(resp, &errBody)
		if errBody.Error != "" {
			return fmt.Errorf("%s", errBody.Error)
		}
		return fmt.Errorf("generate failed (status %d)", resp.StatusCode)
	}
	fmt.Println("generation started — watch the container logs or reload /comet in a moment")
	return nil
}
