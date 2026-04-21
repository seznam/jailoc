package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	dcontainer "github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
)

// ContainerStats holds parsed resource consumption metrics for a container.
type ContainerStats struct {
	CPUPercent float64
	CPULimit   float64 // CPU core limit (0 = unlimited)
	MemUsage   uint64
	MemLimit   uint64
	MemPercent float64
	PIDsCurrent uint64
	PIDsLimit   uint64
	NetRx      uint64
	NetTx      uint64
	BlockRead  uint64
	BlockWrite uint64
	Uptime     time.Duration
}

// ContainerStats fetches a one-shot stats snapshot for the opencode container.
func (c *Client) ContainerStats(ctx context.Context) (ContainerStats, error) {
	containerID, err := c.CurrentContainerID(ctx)
	if err != nil {
		return ContainerStats{}, fmt.Errorf("get container ID for stats: %w", err)
	}
	if containerID == "" {
		return ContainerStats{}, fmt.Errorf("no running opencode container")
	}

	engineCli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		return ContainerStats{}, fmt.Errorf("create Docker Engine client for stats: %w", err)
	}
	defer func() { _ = engineCli.Close() }()

	resp, err := engineCli.ContainerStatsOneShot(ctx, containerID)
	if err != nil {
		return ContainerStats{}, fmt.Errorf("fetch container stats: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var v dcontainer.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return ContainerStats{}, fmt.Errorf("decode container stats: %w", err)
	}

	inspect, err := engineCli.ContainerInspect(ctx, containerID)
	if err != nil {
		return ContainerStats{}, fmt.Errorf("inspect container for uptime: %w", err)
	}

	var uptime time.Duration
	if startedAt, parseErr := time.Parse(time.RFC3339Nano, inspect.State.StartedAt); parseErr == nil {
		uptime = time.Since(startedAt)
	}

	memUsage := calcMemUsage(v.MemoryStats)
	memLimit := v.MemoryStats.Limit
	var memPercent float64
	if memLimit > 0 {
		memPercent = float64(memUsage) / float64(memLimit) * 100.0
	}

	return ContainerStats{
		CPUPercent:  calcCPUPercent(v.PreCPUStats, v.CPUStats),
		CPULimit:    calcCPULimit(inspect.HostConfig.Resources),
		MemUsage:    memUsage,
		MemLimit:    memLimit,
		MemPercent:  memPercent,
		PIDsCurrent: v.PidsStats.Current,
		PIDsLimit:   v.PidsStats.Limit,
		NetRx:       sumNetRx(v.Networks),
		NetTx:       sumNetTx(v.Networks),
		BlockRead:   calcBlockRead(v.BlkioStats),
		BlockWrite:  calcBlockWrite(v.BlkioStats),
		Uptime:      uptime,
	}, nil
}

func calcCPUPercent(pre, cur dcontainer.CPUStats) float64 {
	cpuDelta := float64(cur.CPUUsage.TotalUsage) - float64(pre.CPUUsage.TotalUsage)
	systemDelta := float64(cur.SystemUsage) - float64(pre.SystemUsage)
	if systemDelta <= 0 || cpuDelta < 0 {
		return 0
	}
	onlineCPUs := cur.OnlineCPUs
	if onlineCPUs == 0 {
		n := len(cur.CPUUsage.PercpuUsage)
		if n > 0 {
			onlineCPUs = uint32(n) //nolint:gosec // PercpuUsage length is bounded by physical CPU count
		} else {
			onlineCPUs = 1
		}
	}

	return (cpuDelta / systemDelta) * float64(onlineCPUs) * 100.0
}

func calcCPULimit(r dcontainer.Resources) float64 {
	if r.NanoCPUs > 0 {
		return float64(r.NanoCPUs) / 1e9
	}
	if r.CPUQuota > 0 && r.CPUPeriod > 0 {
		return float64(r.CPUQuota) / float64(r.CPUPeriod)
	}
	return 0
}

func calcMemUsage(m dcontainer.MemoryStats) uint64 {
	// cgroup v2 uses "inactive_file", v1 uses "total_inactive_file".
	if cache, ok := m.Stats["inactive_file"]; ok && cache <= m.Usage {
		return m.Usage - cache
	}
	if cache, ok := m.Stats["total_inactive_file"]; ok && cache <= m.Usage {
		return m.Usage - cache
	}
	return m.Usage
}

func sumNetRx(networks map[string]dcontainer.NetworkStats) uint64 {
	var total uint64
	for _, n := range networks {
		total += n.RxBytes
	}
	return total
}

func sumNetTx(networks map[string]dcontainer.NetworkStats) uint64 {
	var total uint64
	for _, n := range networks {
		total += n.TxBytes
	}
	return total
}

func calcBlockRead(blkio dcontainer.BlkioStats) uint64 {
	var total uint64
	for _, entry := range blkio.IoServiceBytesRecursive {
		if len(entry.Op) > 0 && (entry.Op[0] == 'r' || entry.Op[0] == 'R') {
			total += entry.Value
		}
	}
	return total
}

func calcBlockWrite(blkio dcontainer.BlkioStats) uint64 {
	var total uint64
	for _, entry := range blkio.IoServiceBytesRecursive {
		if len(entry.Op) > 0 && (entry.Op[0] == 'w' || entry.Op[0] == 'W') {
			total += entry.Value
		}
	}
	return total
}

// FormatBytes formats a byte count into a human-readable string (e.g. "1.5 GiB").
func FormatBytes(b uint64) string {
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case b >= gib:
		return fmt.Sprintf("%.1f GiB", float64(b)/float64(gib))
	case b >= mib:
		return fmt.Sprintf("%.1f MiB", float64(b)/float64(mib))
	case b >= kib:
		return fmt.Sprintf("%.1f KiB", float64(b)/float64(kib))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

// FormatUptime formats a duration into a human-readable uptime string.
func FormatUptime(d time.Duration) string {
	d = d.Truncate(time.Second)
	days := int(d.Hours()) / 24
	hours := int(d.Hours()) % 24
	minutes := int(d.Minutes()) % 60
	seconds := int(d.Seconds()) % 60

	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
	case minutes > 0:
		return fmt.Sprintf("%dm %ds", minutes, seconds)
	default:
		return fmt.Sprintf("%ds", seconds)
	}
}
