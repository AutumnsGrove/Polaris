package procmgr

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// SystemdManager manages the process as a Linux systemd service — this
// is the production path on the Le Potato.
type SystemdManager struct {
	label string
}

func newSystemd(label string) (*SystemdManager, error) {
	return &SystemdManager{label: label}, nil
}

func (m *SystemdManager) Name() string { return "systemd" }

func (m *SystemdManager) Install(cfg ServiceConfig) error {
	if err := os.MkdirAll(cfg.LogDir, 0o755); err != nil {
		return fmt.Errorf("creating log directory: %w", err)
	}

	data := unitData{
		Description: "LocalAssistant search agent",
		User:        cfg.User,
		WorkDir:     cfg.WorkDir,
		BinaryPath:  cfg.BinaryPath,
		Path:        cfg.Path,
	}

	tmpl, err := template.New("unit").Parse(unitTemplate)
	if err != nil {
		return fmt.Errorf("parsing unit template: %w", err)
	}

	dest := m.unitPath()
	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("creating unit file %s: %w", dest, err)
	}
	if err := tmpl.Execute(f, data); err != nil {
		f.Close()
		return fmt.Errorf("writing unit file: %w", err)
	}
	f.Close()
	log.Infof("wrote unit: %s", dest)

	if err := m.systemctl("daemon-reload"); err != nil {
		return fmt.Errorf("daemon-reload: %w", err)
	}
	if err := m.systemctl("enable", "--now", m.label); err != nil {
		return fmt.Errorf("enable: %w", err)
	}
	return nil
}

func (m *SystemdManager) Uninstall() error {
	_ = m.systemctl("disable", "--now", m.label)
	dest := m.unitPath()
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing unit file: %w", err)
	}
	_ = m.systemctl("daemon-reload")
	return nil
}

func (m *SystemdManager) Start() error   { return m.systemctl("start", m.label) }
func (m *SystemdManager) Stop() error    { return m.systemctl("stop", m.label) }
func (m *SystemdManager) Restart() error { return m.systemctl("restart", m.label) }

func (m *SystemdManager) IsManaged() bool {
	return exec.Command("systemctl", "is-active", "--quiet", m.label).Run() == nil
}

func (m *SystemdManager) unitPath() string {
	return filepath.Join("/etc/systemd/system", m.label+".service")
}

func (m *SystemdManager) systemctl(args ...string) error {
	out, err := exec.Command("systemctl", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemctl %s (%s): %w", strings.Join(args, " "), strings.TrimSpace(string(out)), err)
	}
	return nil
}

type unitData struct {
	Description string
	User        string
	WorkDir     string
	BinaryPath  string
	Path        string
}

// unitTemplate uses Type=simple (not notify) — we don't ping a systemd
// watchdog in v1, so Restart=always + RestartSec is the safety net
// instead of WatchdogSec.
const unitTemplate = `[Unit]
Description={{.Description}}
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User={{.User}}
Group={{.User}}
WorkingDirectory={{.WorkDir}}
Environment=PATH={{.Path}}
ExecStart={{.BinaryPath}} run

Restart=always
RestartSec=10
StartLimitIntervalSec=600
StartLimitBurst=5


# The app's own logger writes daily-rotated files (logs/YYYY-MM-DD.log,
# 90-day retention — see logger.Init in cmd/run.go). This catches only
# what bypasses that logger entirely: Go runtime panics, output before
# logger.Init runs, etc.
StandardOutput=append:{{.WorkDir}}/logs/service-fallback.log
StandardError=append:{{.WorkDir}}/logs/service-fallback.log

[Install]
WantedBy=multi-user.target
`
