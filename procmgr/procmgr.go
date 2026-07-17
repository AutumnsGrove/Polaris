// Package procmgr abstracts process supervision across platforms.
//
// On macOS, launchd manages the service via a .plist file in
// ~/Library/LaunchAgents. On Linux, systemd manages it via a .service
// unit file in /etc/systemd/system. Both keep the process alive with
// automatic restarts and provide clean start/stop/restart commands.
//
// Ported from her-go's procmgr package — same deployment shape (Mac for
// dev, Le Potato/systemd for production).
package procmgr

import (
	"fmt"
	"os/user"
	"runtime"

	"localassistant/logger"
)

var log = logger.WithPrefix("procmgr")

type Manager interface {
	Install(cfg ServiceConfig) error
	Uninstall() error
	Start() error
	Stop() error
	Restart() error
	IsManaged() bool
	Name() string
}

type ServiceConfig struct {
	Label      string
	BinaryPath string
	WorkDir    string
	LogDir     string
	User       string
	Path       string
}

func New(label string) (Manager, error) {
	switch runtime.GOOS {
	case "darwin":
		return newLaunchd(label)
	case "linux":
		return newSystemd(label)
	default:
		return nil, fmt.Errorf("unsupported platform: %s", runtime.GOOS)
	}
}

func currentUsername() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "unknown"
}
