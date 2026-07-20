package procmgr

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestPlistTemplate_RendersExpectedFields(t *testing.T) {
	out := renderTemplate(t, plistTemplate, plistData{
		Label:      "polaris",
		BinaryPath: "/Users/alice/polaris/polaris",
		WorkDir:    "/Users/alice/polaris",
		StdoutPath: "/Users/alice/polaris/logs/stdout.log",
		StderrPath: "/Users/alice/polaris/logs/stderr.log",
		UserName:   "alice",
		Path:       "/usr/bin:/bin",
	})

	for _, want := range []string{
		"<string>polaris</string>",
		"<string>/Users/alice/polaris/polaris</string>",
		"<string>run</string>",
		"<string>/Users/alice/polaris</string>",
		"<string>/Users/alice/polaris/logs/stdout.log</string>",
		"<string>/Users/alice/polaris/logs/stderr.log</string>",
		"<string>alice</string>",
		"<string>/usr/bin:/bin</string>",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered plist missing %q\n---\n%s", want, out)
		}
	}
}

func TestLaunchdManager_Name(t *testing.T) {
	m := &LaunchdManager{label: "polaris"}
	if m.Name() != "launchd" {
		t.Errorf("Name() = %q, want launchd", m.Name())
	}
}

func TestLaunchdManager_PlistPath(t *testing.T) {
	m := &LaunchdManager{label: "polaris"}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available in this environment: %v", err)
	}
	got, err := m.plistPath()
	if err != nil {
		t.Fatalf("plistPath returned error: %v", err)
	}
	want := home + "/Library/LaunchAgents/polaris.plist"
	if got != want {
		t.Errorf("plistPath() = %q, want %q", got, want)
	}
}

func TestLaunchdManager_ServiceTarget(t *testing.T) {
	m := &LaunchdManager{label: "polaris"}
	want := fmt.Sprintf("gui/%d/polaris", os.Getuid())
	if got := m.serviceTarget(); got != want {
		t.Errorf("serviceTarget() = %q, want %q", got, want)
	}
}
