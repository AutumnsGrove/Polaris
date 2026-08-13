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

// cmdTimeout bounds every quick launchctl/systemctl invocation (start,
// stop, is-active, daemon-reload, ...) — without it, a hung supervisor
// call (e.g. waiting on a D-Bus/polkit lock) blocks install/update/restart
// indefinitely with no way to recover short of killing the process.
const cmdTimeout = 15 * time.Second

// restartCmdTimeout is Restart()'s own, longer bound — `systemctl restart`
// legitimately blocks until the unit's stop phase actually finishes, and
// systemd.go's unit template sets TimeoutStopSec=75 specifically to give
// cmd/run.go's shutdown drain (worst case ~50s: 25s for httpServer.Shutdown
// plus another 25s draining in-flight turns) room to finish on its own
// terms rather than being SIGKILLed mid-drain. cmdTimeout's 15s is far
// short of that: exec.CommandContext killing the *client-side* `sudo
// systemctl restart` process at 15s does NOT stop the actual restart job
// systemd is still running server-side (systemctl only submits the job and
// waits on it — killing the waiting client doesn't cancel the job), so a
// restart that's genuinely still in progress would surface as a spurious
// error here. That error previously raced gateway/update.go's `defer
// release()`: the self-update file lock — held specifically across the
// whole pull+build+restart sequence so a second update can't start while
// this one's binary swap is still happening (see updater.AcquireLock's doc
// comment) — got released the instant Restart() returned "failed", even
// though the real restart could still be minutes from actually landing.
const restartCmdTimeout = 90 * time.Second

// withCmdTimeout returns a context for one quick supervisor subprocess
// call, shared by launchd.go and systemd.go so both platforms bound their
// exec.Command calls the same way.
func withCmdTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), cmdTimeout)
}

// withRestartCmdTimeout is the Restart()-specific counterpart to
// withCmdTimeout — see restartCmdTimeout's doc comment for why a restart
// needs a longer bound than every other supervisor call.
func withRestartCmdTimeout() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), restartCmdTimeout)
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
