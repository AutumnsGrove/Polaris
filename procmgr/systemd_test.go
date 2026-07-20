package procmgr

import (
	"bytes"
	"strings"
	"testing"
	"text/template"
)

// renderTemplate executes an arbitrary text/template against data,
// exactly like Install() does — used here to validate unitTemplate's
// actual generated content without touching disk or systemctl.
func renderTemplate(t *testing.T, tmplSrc string, data interface{}) string {
	t.Helper()
	tmpl, err := template.New("t").Parse(tmplSrc)
	if err != nil {
		t.Fatalf("parsing template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("executing template: %v", err)
	}
	return buf.String()
}

func TestUnitTemplate_RendersExpectedFields(t *testing.T) {
	out := renderTemplate(t, unitTemplate, unitData{
		Description: "Polaris search agent",
		User:        "alice",
		WorkDir:     "/home/alice/polaris",
		BinaryPath:  "/home/alice/polaris/polaris",
		Path:        "/usr/bin:/bin",
	})

	for _, want := range []string{
		"Description=Polaris search agent",
		"User=alice",
		"Group=alice",
		"WorkingDirectory=/home/alice/polaris",
		"Environment=PATH=/usr/bin:/bin",
		"ExecStart=/home/alice/polaris/polaris run",
		"Restart=always",
		"WantedBy=multi-user.target",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered unit file missing %q\n---\n%s", want, out)
		}
	}
}

func TestUnitPath(t *testing.T) {
	m := &SystemdManager{label: "polaris"}
	if got := m.unitPath(); got != "/etc/systemd/system/polaris.service" {
		t.Errorf("unitPath() = %q, want the systemd unit directory", got)
	}
}

func TestSystemdManager_Name(t *testing.T) {
	m := &SystemdManager{label: "polaris"}
	if m.Name() != "systemd" {
		t.Errorf("Name() = %q, want systemd", m.Name())
	}
}
