package procmgr

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

// LaunchdManager manages the process as a macOS launchd service — used
// for local dev on this machine before deploying to the potato.
type LaunchdManager struct {
	label string
}

func newLaunchd(label string) (*LaunchdManager, error) {
	return &LaunchdManager{label: label}, nil
}

func (m *LaunchdManager) Name() string { return "launchd" }

func (m *LaunchdManager) Install(cfg ServiceConfig) error {
	dest, err := m.plistPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.LogDir, 0o755); err != nil {
		return fmt.Errorf("creating log directory: %w", err)
	}

	data := plistData{
		Label:      m.label,
		BinaryPath: cfg.BinaryPath,
		WorkDir:    cfg.WorkDir,
		StdoutPath: filepath.Join(cfg.LogDir, "stdout.log"),
		StderrPath: filepath.Join(cfg.LogDir, "stderr.log"),
		UserName:   cfg.User,
		Path:       cfg.Path,
	}

	tmpl, err := template.New("plist").Parse(plistTemplate)
	if err != nil {
		return fmt.Errorf("parsing plist template: %w", err)
	}

	f, err := os.Create(dest)
	if err != nil {
		return fmt.Errorf("creating plist %s: %w", dest, err)
	}
	if err := tmpl.Execute(f, data); err != nil {
		f.Close()
		return fmt.Errorf("writing plist: %w", err)
	}
	f.Close()
	log.Infof("wrote plist: %s", dest)

	return m.bootstrap(dest)
}

func (m *LaunchdManager) Uninstall() error {
	if err := m.Stop(); err != nil {
		// Not fatal to the uninstall — the plist is still removed below,
		// which is what actually stops launchd from managing it going
		// forward — but worth knowing about: a failed Stop here can mean
		// the process itself keeps running, just no longer supervised.
		log.Warn("stopping service before uninstall failed, continuing", "err", err)
	}
	dest, err := m.plistPath()
	if err != nil {
		return err
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("removing plist: %w", err)
	}
	return nil
}

func (m *LaunchdManager) Start() error {
	dest, err := m.plistPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(dest); os.IsNotExist(err) {
		return fmt.Errorf("plist not found at %s — run install first", dest)
	}
	return m.bootstrap(dest)
}

func (m *LaunchdManager) Stop() error {
	ctx, cancel := withCmdTimeout()
	defer cancel()
	out, err := exec.CommandContext(ctx, "launchctl", "bootout", m.serviceTarget()).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootout (%s): %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (m *LaunchdManager) Restart() error {
	ctx, cancel := withCmdTimeout()
	defer cancel()
	cmd := exec.CommandContext(ctx, "launchctl", "kickstart", "-k", m.serviceTarget())
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl kickstart (%s): %w", strings.TrimSpace(string(out)), err)
	}
	return nil
}

func (m *LaunchdManager) IsManaged() bool {
	ctx, cancel := withCmdTimeout()
	defer cancel()
	err := exec.CommandContext(ctx, "launchctl", "print", m.serviceTarget()).Run()
	if err != nil && ctx.Err() != nil {
		// Timed out or was otherwise cancelled — not the same as "service
		// doesn't exist" (a real answer this method can't get right now).
		// Logged since callers (e.g. cmd/update.go deciding whether to
		// restart) treat any non-nil error identically to "not managed".
		log.Warn("launchctl print timed out or was cancelled while checking IsManaged", "err", ctx.Err())
	}
	return err == nil
}

func (m *LaunchdManager) plistPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("could not determine home directory: %w", err)
	}
	return filepath.Join(home, "Library", "LaunchAgents", m.label+".plist"), nil
}

func (m *LaunchdManager) domainTarget() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

func (m *LaunchdManager) serviceTarget() string {
	return fmt.Sprintf("gui/%d/%s", os.Getuid(), m.label)
}

func (m *LaunchdManager) bootstrap(plistPath string) error {
	domain := m.domainTarget()
	ctx, cancel := withCmdTimeout()
	defer cancel()
	out, err := exec.CommandContext(ctx, "launchctl", "bootstrap", domain, plistPath).CombinedOutput()
	if err == nil {
		return nil
	}

	outStr := strings.TrimSpace(string(out))

	if strings.Contains(outStr, "Bootstrap failed: 5:") ||
		strings.Contains(outStr, "37:") ||
		strings.Contains(outStr, "already loaded") {
		log.Info("service already loaded, reloading")
		bootoutCtx, bootoutCancel := withCmdTimeout()
		if bootoutErr := exec.CommandContext(bootoutCtx, "launchctl", "bootout", m.serviceTarget()).Run(); bootoutErr != nil {
			// Not fatal on its own — the bootstrap retry right below is
			// what actually matters — but if that retry also fails, this
			// is very likely why, so it needs to be on record rather than
			// silently discarded.
			log.Warn("launchctl bootout before bootstrap retry failed", "err", bootoutErr)
		}
		bootoutCancel()

		retryCtx, retryCancel := withCmdTimeout()
		defer retryCancel()
		out2, err2 := exec.CommandContext(retryCtx, "launchctl", "bootstrap", domain, plistPath).CombinedOutput()
		if err2 != nil {
			return fmt.Errorf("launchctl bootstrap retry (%s): %w", strings.TrimSpace(string(out2)), err2)
		}
		return nil
	}

	return fmt.Errorf("launchctl bootstrap (%s): %w", outStr, err)
}

type plistData struct {
	Label      string
	BinaryPath string
	WorkDir    string
	StdoutPath string
	StderrPath string
	UserName   string
	Path       string
}

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>{{.Label}}</string>

    <key>ProgramArguments</key>
    <array>
        <string>{{.BinaryPath}}</string>
        <string>run</string>
    </array>

    <key>WorkingDirectory</key>
    <string>{{.WorkDir}}</string>

    <key>KeepAlive</key>
    <true/>

    <key>ThrottleInterval</key>
    <integer>3</integer>

    <key>StandardOutPath</key>
    <string>{{.StdoutPath}}</string>

    <key>StandardErrorPath</key>
    <string>{{.StderrPath}}</string>

    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>{{.Path}}</string>
    </dict>

    <key>UserName</key>
    <string>{{.UserName}}</string>
</dict>
</plist>
`
