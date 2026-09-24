package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"polaris/gateway"
)

var searchModel string

var searchCmd = &cobra.Command{
	Use:   "search [query]",
	Short: "Run a search-augmented query straight from the terminal, no web UI needed",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runSearch,
}

func init() {
	searchCmd.Flags().StringVarP(&searchModel, "model", "m", "", "model id (defaults to default_model)")
	rootCmd.AddCommand(searchCmd)
}

func runSearch(cmd *cobra.Command, args []string) error {
	return runDockerSearch(strings.Join(args, " "), searchModel)
}

// runDockerSearch POSTs the query to the running container's own
// /api/ask (the same synchronous, non-streaming endpoint any
// programmatic caller uses — see gateway/ask.go's doc comment). No live
// "thinking"/tool-call progress lines — /api/ask blocks until the whole
// turn finishes, so there's genuinely nothing to print until then.
func runDockerSearch(query, model string) error {
	reqBody, err := json.Marshal(gateway.AskRequest{Content: query, Model: model, Source: "cli"})
	if err != nil {
		return err
	}

	url := dockerLocalBaseURL() + "/api/ask"
	// A real research turn (several search/read rounds) can legitimately
	// take a couple of minutes — same reasoning as runDockerModeCall's
	// generous timeout.
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Post(url, "application/json", bytes.NewReader(reqBody))
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
		return fmt.Errorf("query failed (status %d)", resp.StatusCode)
	}

	var ask gateway.AskResponse
	if err := readCappedJSON(resp, &ask); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}

	fmt.Println(ask.Answer)
	if len(ask.Citations) > 0 {
		fmt.Println("\nSources:")
		for _, c := range ask.Citations {
			fmt.Printf("  - %s (%s)\n", c.Title, c.URL)
		}
	}
	fmt.Printf("\ncost: $%.5f\n", ask.CostUSD)
	return nil
}
