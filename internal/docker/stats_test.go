package docker

import (
	"testing"
	"time"

	dcontainer "github.com/docker/docker/api/types/container"
)

func TestCalcCPUPercent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		pre  dcontainer.CPUStats
		cur  dcontainer.CPUStats
		want float64
	}{
		{
			name: "normal usage with 4 CPUs",
			pre: dcontainer.CPUStats{
				CPUUsage:    dcontainer.CPUUsage{TotalUsage: 100_000_000},
				SystemUsage: 1_000_000_000,
			},
			cur: dcontainer.CPUStats{
				CPUUsage:    dcontainer.CPUUsage{TotalUsage: 200_000_000},
				SystemUsage: 2_000_000_000,
				OnlineCPUs:  4,
			},
			want: 40.0,
		},
		{
			name: "zero system delta returns zero",
			pre: dcontainer.CPUStats{
				CPUUsage:    dcontainer.CPUUsage{TotalUsage: 100},
				SystemUsage: 500,
			},
			cur: dcontainer.CPUStats{
				CPUUsage:    dcontainer.CPUUsage{TotalUsage: 200},
				SystemUsage: 500,
				OnlineCPUs:  2,
			},
			want: 0,
		},
		{
			name: "zero CPU delta returns zero",
			pre: dcontainer.CPUStats{
				CPUUsage:    dcontainer.CPUUsage{TotalUsage: 100},
				SystemUsage: 500,
			},
			cur: dcontainer.CPUStats{
				CPUUsage:    dcontainer.CPUUsage{TotalUsage: 100},
				SystemUsage: 1000,
				OnlineCPUs:  1,
			},
			want: 0,
		},
		{
			name: "OnlineCPUs zero falls back to PercpuUsage length",
			pre: dcontainer.CPUStats{
				CPUUsage:    dcontainer.CPUUsage{TotalUsage: 100_000_000},
				SystemUsage: 1_000_000_000,
			},
			cur: dcontainer.CPUStats{
				CPUUsage: dcontainer.CPUUsage{
					TotalUsage:  200_000_000,
					PercpuUsage: []uint64{0, 0, 0, 0},
				},
				SystemUsage: 2_000_000_000,
				OnlineCPUs:  0,
			},
			want: 40.0,
		},
		{
			name: "OnlineCPUs zero and empty PercpuUsage falls back to 1",
			pre: dcontainer.CPUStats{
				CPUUsage:    dcontainer.CPUUsage{TotalUsage: 100_000_000},
				SystemUsage: 1_000_000_000,
			},
			cur: dcontainer.CPUStats{
				CPUUsage:    dcontainer.CPUUsage{TotalUsage: 200_000_000},
				SystemUsage: 2_000_000_000,
				OnlineCPUs:  0,
			},
			want: 10.0,
		},
	}

	const epsilon = 1e-9

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := calcCPUPercent(tc.pre, tc.cur)
			if diff := got - tc.want; diff > epsilon || diff < -epsilon {
				t.Fatalf("got %.2f, want %.2f", got, tc.want)
			}
		})
	}
}

func TestCalcMemUsage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mem  dcontainer.MemoryStats
		want uint64
	}{
		{
			name: "cgroup v2 subtracts inactive_file",
			mem: dcontainer.MemoryStats{
				Usage: 1000,
				Stats: map[string]uint64{"inactive_file": 200},
			},
			want: 800,
		},
		{
			name: "cgroup v1 subtracts total_inactive_file",
			mem: dcontainer.MemoryStats{
				Usage: 1000,
				Stats: map[string]uint64{"total_inactive_file": 300},
			},
			want: 700,
		},
		{
			name: "no cache stats returns raw usage",
			mem: dcontainer.MemoryStats{
				Usage: 500,
				Stats: map[string]uint64{},
			},
			want: 500,
		},
		{
			name: "cache exceeds usage returns raw usage",
			mem: dcontainer.MemoryStats{
				Usage: 100,
				Stats: map[string]uint64{"inactive_file": 200},
			},
			want: 100,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := calcMemUsage(tc.mem)
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestSumNetRxTx(t *testing.T) {
	t.Parallel()

	networks := map[string]dcontainer.NetworkStats{
		"eth0": {RxBytes: 100, TxBytes: 200},
		"eth1": {RxBytes: 300, TxBytes: 400},
	}

	if got := sumNetRx(networks); got != 400 {
		t.Fatalf("sumNetRx: got %d, want 400", got)
	}
	if got := sumNetTx(networks); got != 600 {
		t.Fatalf("sumNetTx: got %d, want 600", got)
	}
}

func TestSumNetEmpty(t *testing.T) {
	t.Parallel()

	if got := sumNetRx(nil); got != 0 {
		t.Fatalf("sumNetRx(nil): got %d, want 0", got)
	}
	if got := sumNetTx(nil); got != 0 {
		t.Fatalf("sumNetTx(nil): got %d, want 0", got)
	}
}

func TestCalcBlockReadWrite(t *testing.T) {
	t.Parallel()

	blkio := dcontainer.BlkioStats{
		IoServiceBytesRecursive: []dcontainer.BlkioStatEntry{
			{Op: "read", Value: 1024},
			{Op: "Read", Value: 2048},
			{Op: "write", Value: 512},
			{Op: "Write", Value: 256},
			{Op: "sync", Value: 999},
		},
	}

	if got := calcBlockRead(blkio); got != 3072 {
		t.Fatalf("calcBlockRead: got %d, want 3072", got)
	}
	if got := calcBlockWrite(blkio); got != 768 {
		t.Fatalf("calcBlockWrite: got %d, want 768", got)
	}
}

func TestCalcBlockEmpty(t *testing.T) {
	t.Parallel()

	blkio := dcontainer.BlkioStats{}
	if got := calcBlockRead(blkio); got != 0 {
		t.Fatalf("calcBlockRead(empty): got %d, want 0", got)
	}
	if got := calcBlockWrite(blkio); got != 0 {
		t.Fatalf("calcBlockWrite(empty): got %d, want 0", got)
	}
}

func TestCalcCPULimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		res  dcontainer.Resources
		want float64
	}{
		{
			name: "NanoCPUs set to 2 cores",
			res:  dcontainer.Resources{NanoCPUs: 2_000_000_000},
			want: 2.0,
		},
		{
			name: "CPUQuota and CPUPeriod set to 0.5 cores",
			res:  dcontainer.Resources{CPUQuota: 50000, CPUPeriod: 100000},
			want: 0.5,
		},
		{
			name: "NanoCPUs takes precedence over quota/period",
			res:  dcontainer.Resources{NanoCPUs: 1_000_000_000, CPUQuota: 50000, CPUPeriod: 100000},
			want: 1.0,
		},
		{
			name: "no limits returns zero",
			res:  dcontainer.Resources{},
			want: 0,
		},
	}

	const epsilon = 1e-9

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := calcCPULimit(tc.res)
			if diff := got - tc.want; diff > epsilon || diff < -epsilon {
				t.Fatalf("got %.2f, want %.2f", got, tc.want)
			}
		})
	}
}

func TestFormatBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input uint64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1048576, "1.0 MiB"},
		{1073741824, "1.0 GiB"},
		{1610612736, "1.5 GiB"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			got := FormatBytes(tc.input)
			if got != tc.want {
				t.Fatalf("FormatBytes(%d): got %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestFormatUptime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input time.Duration
		want  string
	}{
		{5 * time.Second, "5s"},
		{90 * time.Second, "1m 30s"},
		{3661 * time.Second, "1h 1m 1s"},
		{90061 * time.Second, "1d 1h 1m"},
	}

	for _, tc := range tests {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()

			got := FormatUptime(tc.input)
			if got != tc.want {
				t.Fatalf("FormatUptime(%v): got %q, want %q", tc.input, got, tc.want)
			}
		})
	}
}
