package agentclient

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/vpsmanager/backend/internal/agentwire"
	"golang.org/x/sys/unix"
)

func bootID() (string, error) {
	b, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(b)), err
}

func systemUptime() (uint64, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	var seconds float64
	if _, err = fmt.Sscan(string(b), &seconds); err != nil || seconds < 0 {
		return 0, fmt.Errorf("invalid system uptime")
	}
	return uint64(seconds), nil
}

func lockStateFile(f *os.File) error         { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func replaceStateFile(from, to string) error { return os.Rename(from, to) }
func syncDirectory(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

func runDaemon(c agentwire.Config, version, _ string) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return Run(ctx, c, version)
}

// CollectSwapMemory reports operating-system-specific swap or paging-file usage.
func CollectSwapMemory() (*mem.SwapMemoryStat, error) { return mem.SwapMemory() }
