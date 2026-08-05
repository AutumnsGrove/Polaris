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
	"context"
	"fmt"
	"runtime"
	"time"

	"polaris/logger"
)

var log = logger.WithPrefix("procmgr")

// cmdTimeout bounds every launchctl/systemctl invocation — without it, a
// hung supervisor call (e.g. waiting on a D-Bus/polkit lock) blocks
// install/update/restart indefinitely with no way to recover short of
// killing the process.
const cmdTimeout = 15 * time.Second

// withCmdTimeout returns a context for one supervisor subprocess call,
// shared by launchd.go and systemd.go so both platforms bound their
// exec.Command calls the same way.
func withCmdTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cmdTimeout)
}

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

