package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// isDockerComposeInstall reports whether repoPath (updater.RepoPath's
// CWD, the same directory bare-metal's own update/restart logic
// already assumes it's running from) is a Docker Compose install —
// docker-compose.yml existing there is the one signal a process
// running on the host, outside any container, can actually observe.
// gateway.deploymentMode's POLARIS_DEPLOYMENT env var check answers a
// different question ("is THIS process running inside the container")
// and is never set in a host-side SSH session regardless of which way
// the install actually runs, so it can't be reused here.
func isDockerComposeInstall(repoPath string) bool {
	_, err := os.Stat(filepath.Join(repoPath, "docker-compose.yml"))
	return err == nil
}

// dockerLocalBaseURL is where the Docker deployment's own polaris
// process listens on the host — docker-compose.yml publishes
// 127.0.0.1:${POLARIS_PORT:-8899}:8899, so this SSH CLI (running on
// the host, outside any container) reaches it the exact same way a
// browser hitting the settings panel would. POLARIS_PORT overrides
// the port to match a non-default setup, same env var convention
// docker-compose.yml itself already uses.
func dockerLocalBaseURL() string {
	port := os.Getenv("POLARIS_PORT")
	if port == "" {
		port = "8899"
	}
	return "http://127.0.0.1:" + port
}

// runDockerModeCall is polaris update/restart's Docker-mode
// implementation: POST to the already-running local container's own
// /api/update or /api/restart, which does the real work (GHCR digest
// resolution, waiting out an in-progress CI build, writing the host
// watcher's signal file — see gateway/docker_update.go and
// gateway/docker_ci_status.go). Reusing that endpoint instead of
// duplicating its logic here is deliberate: the CLI and the settings
// panel button must never be able to drift apart in behavior, and
// before this existed, the CLI didn't know Docker mode was a thing at
// all — `polaris update` over SSH on a Docker deployment silently
// rebuilt an orphaned bare-metal binary nothing used and never touched
// the actual running container.
//
// The long client timeout matters: gateway's waitForPublishWorkflow
// can legitimately hold the HTTP response for minutes if "Update
// Polaris" is triggered while docker-publish.yml is still building —
// this call needs to outlast that, not time out mid-wait and report a
// false failure.
func runDockerModeCall(endpoint string) error {
	url := dockerLocalBaseURL() + endpoint
	client := &http.Client{Timeout: 8 * time.Minute}
	resp, err := client.Post(url, "application/json", nil)
	if err != nil {
		return fmt.Errorf("reaching the local polaris server at %s: %w (is the container running? try `docker compose ps`)", url, err)
	}
	defer resp.Body.Close()

	var body struct {
		Success        bool   `json:"success"`
		Log            string `json:"log"`
		Error          string `json:"error"`
		AlreadyRunning bool   `json:"already_running"`
		Restarting     bool   `json:"restarting"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("decoding response from %s: %w", url, err)
	}

	if !body.Success {
		if body.AlreadyRunning {
			return fmt.Errorf("an update or restart is already in progress")
		}
		return fmt.Errorf("%s", body.Error)
	}

	fmt.Println(body.Log)
	if body.Restarting {
		fmt.Println("the host update watcher will pull and restart the container shortly")
	}
	return nil
}
