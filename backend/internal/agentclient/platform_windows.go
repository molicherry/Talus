package agentclient

import (
	"os"
	"unsafe"

	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"golang.org/x/sys/windows"
)

// The kernel boot GUID survives agent restarts and wall-clock adjustments, and
// changes on reboot. Deriving it from wall time minus uptime can split one boot.
func bootID() (string, error) {
	var info struct {
		BootIdentifier windows.GUID
		FirmwareType   uint32
		BootFlags      uint64
	}
	const systemBootEnvironmentInformation = 90
	err := windows.NtQuerySystemInformation(systemBootEnvironmentInformation, unsafe.Pointer(&info), uint32(unsafe.Sizeof(info)), nil)
	if err != nil {
		return "", err
	}
	return info.BootIdentifier.String(), nil
}

func systemUptime() (uint64, error) { return host.Uptime() }

func lockStateFile(f *os.File) error {
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &windows.Overlapped{})
}

func replaceStateFile(from, to string) error {
	f, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	t, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(f, t, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// Windows does not support fsync on directory handles. Each file is flushed
// before publishing and MoveFileEx uses write-through above.
func syncDirectory(string) error { return nil }

// CollectSwapMemory enumerates actual paging files. Windows commit limits are
// not paging-file capacity; subtracting physical memory can even underflow when
// paging files are disabled. Enumeration also avoids localized PDH counter names.
func CollectSwapMemory() (*mem.SwapMemoryStat, error) {
	devices, err := mem.SwapDevices()
	if err != nil {
		return nil, err
	}
	s := &mem.SwapMemoryStat{}
	for _, d := range devices {
		s.Used += d.UsedBytes
		s.Free += d.FreeBytes
	}
	s.Total = s.Used + s.Free
	if s.Total > 0 {
		s.UsedPercent = 100 * float64(s.Used) / float64(s.Total)
	}
	return s, nil
}
