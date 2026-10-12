package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// exportCmd is a thin client over the running container's own
// GET /api/threads/{id}/export (gateway/thread_export.go), the same
// endpoint the thread menu's Export modal uses — so the CLI and the UI can
// never drift apart in what an export contains. It writes the rendered
// file to stdout (pipe-friendly) or to --out.
var (
	exportFormat      string
	exportScope       string
	exportSources     bool
	exportAttachments bool
	exportMeta        bool
	exportTrail       bool
	exportThinking    bool
	exportPrivate     bool
	exportPrivatePers bool
	exportOut         string
)

var exportCmd = &cobra.Command{
	Use:   "export <thread-id>",
	Short: "Download one thread as Markdown or JSON",
	Args:  cobra.ExactArgs(1),
	RunE:  runExport,
}

func init() {
	exportCmd.Flags().StringVar(&exportFormat, "format", "md", "output format: md or json")
	exportCmd.Flags().StringVar(&exportScope, "scope", "full", "full, or handoff (the stored summary plus later messages)")
	exportCmd.Flags().BoolVar(&exportSources, "sources", true, "include citations and the source list")
	exportCmd.Flags().BoolVar(&exportAttachments, "attachments", false, "include attachment names and types")
	exportCmd.Flags().BoolVar(&exportMeta, "meta", false, "include cost and model metadata")
	exportCmd.Flags().BoolVar(&exportTrail, "trail", false, "include the research trail (tool calls and results)")
	exportCmd.Flags().BoolVar(&exportThinking, "thinking", false, "include thinking traces")
	exportCmd.Flags().BoolVar(&exportPrivate, "private", true, "leave out memory tool calls (pass --private=false to include them)")
	exportCmd.Flags().BoolVar(&exportPrivatePers, "private-personal", false, "also leave out chat search, star reads and save-to-field")
	exportCmd.Flags().StringVarP(&exportOut, "out", "o", "", "write to this file instead of stdout")
	rootCmd.AddCommand(exportCmd)
}

func runExport(cmd *cobra.Command, args []string) error {
	q := url.Values{}
	q.Set("format", exportFormat)
	q.Set("scope", exportScope)
	q.Set("sources", boolParam(exportSources))
	q.Set("attachments", boolParam(exportAttachments))
	q.Set("meta", boolParam(exportMeta))
	q.Set("trail", boolParam(exportTrail))
	q.Set("thinking", boolParam(exportThinking))
	q.Set("private", boolParam(exportPrivate))
	q.Set("private_personal", boolParam(exportPrivatePers))

	u := fmt.Sprintf("%s/api/threads/%s/export?%s", dockerLocalBaseURL(), url.PathEscape(args[0]), q.Encode())

	// A long researched thread has to be gathered and rendered, but nothing
	// here spends money or waits on an LLM — a couple of minutes is ample.
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Get(u)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", u, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("export failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	out := io.Writer(os.Stdout)
	if exportOut != "" {
		f, err := os.Create(exportOut)
		if err != nil {
			return fmt.Errorf("creating %s: %w", exportOut, err)
		}
		defer f.Close()
		out = f
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("writing export: %w", err)
	}
	return nil
}

func boolParam(b bool) string {
	if b {
		return "1"
	}
	return "0"
}
