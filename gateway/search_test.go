package gateway

import (
	"net/http"
	"strings"
	"testing"

	"polaris/search"
)

func TestHandleSetDomainRanking_WritesThroughToTheConfiguredFile(t *testing.T) {
	h := newTestHarness(t, "")

	req, err := http.NewRequest(http.MethodPut, h.url("/api/domain-rankings"), strings.NewReader(`{"domain":"reddit.com","state":"raise"}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/domain-rankings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", resp.StatusCode)
	}

	path := h.srvObj.searxng.DomainRankingsPath()
	if path == "" {
		t.Fatal("expected the harness's SearXNGClient to have a domain rankings path configured")
	}
	if got := search.LoadDomainRankings(path).State("https://reddit.com"); got != search.RankRaise {
		t.Errorf("State(reddit.com) = %q, want raise", got)
	}
}

func TestHandleSetDomainRanking_RejectsInvalidState(t *testing.T) {
	h := newTestHarness(t, "")

	req, err := http.NewRequest(http.MethodPut, h.url("/api/domain-rankings"), strings.NewReader(`{"domain":"reddit.com","state":"boost"}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/domain-rankings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for an invalid state", resp.StatusCode)
	}
}

func TestHandleSetDomainRanking_RejectsMissingDomain(t *testing.T) {
	h := newTestHarness(t, "")

	req, err := http.NewRequest(http.MethodPut, h.url("/api/domain-rankings"), strings.NewReader(`{"state":"raise"}`))
	if err != nil {
		t.Fatalf("building request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("PUT /api/domain-rankings: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 when domain is missing", resp.StatusCode)
	}
}
