package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
)

// AgentOutput contains all collected system metrics.
type AgentOutput struct {
	Timestamp  string      `json:"timestamp"`
	Hostname   string      `json:"hostname"`
	CPU        CPUInfo     `json:"cpu"`
	Memory     MemoryInfo  `json:"memory"`
	Swap       *SwapInfo   `json:"swap,omitempty"`
	Disks      []DiskInfo  `json:"disks"`
	DiskIO     *DiskIOInfo `json:"disk_io,omitempty"`
	NetIO      *NetIOInfo  `json:"net_io,omitempty"`
	OS         OSInfo      `json:"os"`
	UptimeSecs uint64      `json:"uptime_seconds"`
}

type CPUInfo struct {
	Percent float64 `json:"percent"`
	Cores   int     `json:"cores"`
	Model   string  `json:"model"`
	Load1   float64 `json:"load_1"`
	Load5   float64 `json:"load_5"`
	Load15  float64 `json:"load_15"`
}

type MemoryInfo struct {
	UsedBytes  uint64  `json:"used_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	Percent    float64 `json:"percent"`
}

type SwapInfo struct {
	UsedBytes  uint64  `json:"used_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	Percent    float64 `json:"percent"`
}

type DiskInfo struct {
	Mount      string  `json:"mount"`
	Device     string  `json:"device"`
	UsedBytes  uint64  `json:"used_bytes"`
	TotalBytes uint64  `json:"total_bytes"`
	Percent    float64 `json:"percent"`
}

type DiskIOInfo struct {
	ReadBytes  uint64 `json:"read_bytes"`
	WriteBytes uint64 `json:"write_bytes"`
}

type NetIOInfo struct {
	BytesRecv   uint64 `json:"bytes_recv"`
	BytesSent   uint64 `json:"bytes_sent"`
	PacketsRecv uint64 `json:"packets_recv"`
	PacketsSent uint64 `json:"packets_sent"`
}

type OSInfo struct {
	OS         string `json:"os"`
	Platform   string `json:"platform"`
	KernelArch string `json:"kernel_arch"`
	KernelVer  string `json:"kernel_version"`
}

func main() {
	collectCPU := flag.Bool("cpu", true, "collect CPU metrics")
	collectMemory := flag.Bool("memory", true, "collect memory metrics")
	collectDisk := flag.Bool("disk", true, "collect disk metrics")
	collectSwap := flag.Bool("swap", true, "collect swap metrics")
	collectNetIO := flag.Bool("netio", true, "collect network I/O counters")
	collectDiskIO := flag.Bool("diskio", true, "collect disk I/O counters")
	format := flag.String("format", "json", "output format (json)")
	flag.Parse()

	output := AgentOutput{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	hostname, err := os.Hostname()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to get hostname: %v\n", err)
		os.Exit(1)
	}
	output.Hostname = hostname

	// OS info — always collected, lightweight.
	osInfo, err := collectOSInfo()
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to collect OS info: %v\n", err)
		os.Exit(1)
	}
	output.OS = osInfo

	uptime, err := host.Uptime()
	if err == nil {
		output.UptimeSecs = uptime
	}

	if *collectCPU {
		info, err := collectCPUInfo()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to collect CPU metrics: %v\n", err)
			os.Exit(1)
		}
		output.CPU = info
	}

	if *collectMemory {
		info, err := collectMemoryInfo()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to collect memory metrics: %v\n", err)
			os.Exit(1)
		}
		output.Memory = info
	}

	if *collectSwap {
		info, err := collectSwapInfo()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to collect swap metrics: %v\n", err)
			os.Exit(1)
		}
		if info != nil {
			output.Swap = info
		}
	}

	if *collectDisk {
		infos, err := collectDiskInfo()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to collect disk metrics: %v\n", err)
			os.Exit(1)
		}
		output.Disks = infos
	}

	if *collectNetIO {
		info, err := collectNetIOInfo()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to collect network I/O: %v\n", err)
			os.Exit(1)
		}
		output.NetIO = info
	}

	if *collectDiskIO {
		info, err := collectDiskIOInfo()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to collect disk I/O: %v\n", err)
			os.Exit(1)
		}
		output.DiskIO = info
	}

	switch *format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(output); err != nil {
			fmt.Fprintf(os.Stderr, "failed to encode output: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unsupported format: %s\n", *format)
		os.Exit(1)
	}
}

func collectOSInfo() (OSInfo, error) {
	info, err := host.Info()
	if err != nil {
		return OSInfo{}, fmt.Errorf("host info: %w", err)
	}
	return OSInfo{
		OS:         info.OS,
		Platform:   info.Platform + " " + info.PlatformVersion,
		KernelArch: info.KernelArch,
		KernelVer:  info.KernelVersion,
	}, nil
}

func collectCPUInfo() (CPUInfo, error) {
	info := CPUInfo{}

	cpuInfos, err := cpu.Info()
	if err == nil && len(cpuInfos) > 0 {
		info.Model = cpuInfos[0].ModelName
	}

	logical, err := cpu.Counts(true)
	if err != nil {
		return info, fmt.Errorf("cpu count: %w", err)
	}
	info.Cores = logical

	percentages, err := cpu.Percent(1*time.Second, false)
	if err != nil {
		return info, fmt.Errorf("cpu percent: %w", err)
	}
	if len(percentages) > 0 {
		info.Percent = percentages[0]
	}

	loadAvg, err := load.Avg()
	if err != nil {
		return info, fmt.Errorf("load avg: %w", err)
	}
	info.Load1 = loadAvg.Load1
	info.Load5 = loadAvg.Load5
	info.Load15 = loadAvg.Load15

	return info, nil
}

func collectMemoryInfo() (MemoryInfo, error) {
	info := MemoryInfo{}

	vmem, err := mem.VirtualMemory()
	if err != nil {
		return info, fmt.Errorf("virtual memory: %w", err)
	}
	info.UsedBytes = vmem.Used
	info.TotalBytes = vmem.Total
	info.Percent = vmem.UsedPercent

	return info, nil
}

func collectSwapInfo() (*SwapInfo, error) {
	swap, err := mem.SwapMemory()
	if err != nil {
		return nil, fmt.Errorf("swap memory: %w", err)
	}
	if swap.Total == 0 {
		return nil, nil
	}
	return &SwapInfo{
		UsedBytes:  swap.Used,
		TotalBytes: swap.Total,
		Percent:    swap.UsedPercent,
	}, nil
}

func collectDiskInfo() ([]DiskInfo, error) {
	partitions, err := disk.Partitions(false)
	if err != nil {
		return nil, fmt.Errorf("disk partitions: %w", err)
	}

	var disks []DiskInfo
	for _, p := range partitions {
		if isPseudoFS(p.Fstype) {
			continue
		}
		usage, err := disk.Usage(p.Mountpoint)
		if err != nil {
			continue
		}
		disks = append(disks, DiskInfo{
			Mount:      p.Mountpoint,
			Device:     p.Device,
			UsedBytes:  usage.Used,
			TotalBytes: usage.Total,
			Percent:    usage.UsedPercent,
		})
	}
	return disks, nil
}

func collectNetIOInfo() (*NetIOInfo, error) {
	counters, err := net.IOCounters(false)
	if err != nil {
		return nil, fmt.Errorf("net io counters: %w", err)
	}

	var total NetIOInfo
	for _, c := range counters {
		if c.Name == "lo" {
			continue
		}
		total.BytesRecv += c.BytesRecv
		total.BytesSent += c.BytesSent
		total.PacketsRecv += c.PacketsRecv
		total.PacketsSent += c.PacketsSent
	}
	return &total, nil
}

func collectDiskIOInfo() (*DiskIOInfo, error) {
	counters, err := disk.IOCounters()
	if err != nil {
		return nil, fmt.Errorf("disk io counters: %w", err)
	}

	var total DiskIOInfo
	for _, c := range counters {
		if isLoopDevice(c.Name) {
			continue
		}
		total.ReadBytes += c.ReadBytes
		total.WriteBytes += c.WriteBytes
	}
	return &total, nil
}

func isPseudoFS(fstype string) bool {
	switch fstype {
	case "tmpfs", "devtmpfs", "devfs", "proc", "sysfs", "securityfs",
		"cgroup", "cgroup2", "pstore", "hugetlbfs", "configfs", "debugfs",
		"tracefs", "fusectl", "overlay", "squashfs":
		return true
	}
	return false
}

func isLoopDevice(name string) bool {
	// Filter out loop devices, ram disks, and dm- devices.
	for _, prefix := range []string{"loop", "ram", "dm-", "zram"} {
		if len(name) >= len(prefix) && name[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
