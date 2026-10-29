//go:build linux

package guest

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/stacklok/go-microvm/guest/harden"
)

const (
	zramDevice     = "/dev/zram0"
	zramSys        = "/sys/block/zram0"
	cgroupReclaim  = "/sys/fs/cgroup/memory.reclaim"
	reclaimEvery   = time.Minute
	zramSwappiness = "100"
	// swapFlagDiscard is SWAP_FLAG_DISCARD from linux/swap.h: zram frees
	// the memory of swap slots the kernel discards.
	swapFlagDiscard = 0x10000
)

func readMeminfo() (meminfo, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return nil, fmt.Errorf("open meminfo: %w", err)
	}
	defer func() { _ = f.Close() }()
	return parseMeminfo(f)
}

// setupZram enables a zstd-compressed swap device in RAM sized at half the
// guest memory: idle anonymous memory is compressed instead of holding
// whole pages.
func setupZram(logger *slog.Logger) error {
	info, err := readMeminfo()
	if err != nil {
		return err
	}
	if _, err := os.Stat(zramSys); err != nil {
		return fmt.Errorf("zram device: %w", err)
	}
	// The algorithm must be chosen before the size; lzo-rle stays when the
	// kernel lacks zstd.
	if err := os.WriteFile(zramSys+"/comp_algorithm", []byte("zstd"), 0); err != nil {
		logger.Warn("zram: zstd unavailable", "error", err)
	}
	size := info["MemTotal"] / 2
	if err := os.WriteFile(zramSys+"/disksize", []byte(strconv.FormatUint(size, 10)), 0); err != nil {
		return fmt.Errorf("zram disksize: %w", err)
	}
	header, err := swapHeader(os.Getpagesize(), size)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(zramDevice, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("open %s: %w", zramDevice, err)
	}
	if _, err := f.Write(header); err != nil {
		_ = f.Close()
		return fmt.Errorf("write swap header: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close %s: %w", zramDevice, err)
	}
	path, err := unix.BytePtrFromString(zramDevice)
	if err != nil {
		return fmt.Errorf("swapon: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_SWAPON, uintptr(unsafe.Pointer(path)), swapFlagDiscard, 0); errno != 0 { //nolint:gosec // G103: x/sys has no swapon wrapper
		return fmt.Errorf("swapon %s: %w", zramDevice, errno)
	}
	// Swapping to RAM is cheap and reads back one page at a time.
	for key, value := range map[string]string{"vm.swappiness": zramSwappiness, "vm.page-cluster": "0"} {
		if err := harden.Set(key, value); err != nil {
			logger.Warn("set "+key, "error", err)
		}
	}
	logger.Info("zram swap enabled", "size_mib", size>>20)
	return nil
}

// startReclaim reclaims page cache once a minute, so memory the guest no
// longer uses goes back to the host. It returns a function that stops it.
func startReclaim(logger *slog.Logger) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(reclaimEvery)
		defer t.Stop()
		cgroup := true
		for {
			select {
			case <-stop:
				return
			case <-t.C:
			}
			info, err := readMeminfo()
			if err != nil {
				logger.Warn("memory reclaim", "error", err)
				continue
			}
			target := reclaimTarget(info)
			if target == 0 {
				continue
			}
			if cgroup {
				err := os.WriteFile(cgroupReclaim, []byte(strconv.FormatUint(target, 10)), 0)
				// EAGAIN: the kernel reclaimed less than asked.
				if err == nil || errors.Is(err, syscall.EAGAIN) {
					continue
				}
				logger.Warn("memory.reclaim unavailable; dropping clean caches instead", "error", err)
				cgroup = false
			}
			if err := os.WriteFile("/proc/sys/vm/drop_caches", []byte("1"), 0); err != nil {
				logger.Warn("drop caches", "error", err)
			}
		}
	}()
	return func() {
		close(stop)
		<-done
	}
}
