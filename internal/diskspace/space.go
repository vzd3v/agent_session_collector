// Package diskspace implements start-time filesystem admission checks. These
// checks are not reservations: concurrent writers still require OS quotas.
package diskspace

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"syscall"
)

func Available(name string) (int64, error) {
	for {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(name, &stat); err == nil {
			if stat.Bsize <= 0 || stat.Bavail > uint64(math.MaxInt64)/uint64(stat.Bsize) {
				return math.MaxInt64, nil
			}
			return int64(stat.Bavail) * stat.Bsize, nil
		} else if !os.IsNotExist(err) {
			return 0, fmt.Errorf("stat filesystem: %w", err)
		}
		parent := filepath.Dir(name)
		if parent == name {
			return 0, fmt.Errorf("no existing ancestor for filesystem check")
		}
		name = parent
	}
}

func SameFilesystem(a, b string) (bool, error) {
	device := func(name string) (uint64, error) {
		for {
			var stat syscall.Stat_t
			err := syscall.Stat(name, &stat)
			if err == nil {
				return uint64(stat.Dev), nil
			}
			if !os.IsNotExist(err) {
				return 0, err
			}
			parent := filepath.Dir(name)
			if parent == name {
				return 0, err
			}
			name = parent
		}
	}
	da, err := device(a)
	if err != nil {
		return false, err
	}
	db, err := device(b)
	return da == db, err
}

func Check(name string, floor, expected int64) error {
	available, err := Available(name)
	if err != nil {
		return err
	}
	if expected < 0 || floor < 0 || floor > math.MaxInt64-expected || available < floor+expected {
		return fmt.Errorf("disk pressure: available=%d min_free=%d expected_write=%d", available, floor, expected)
	}
	return nil
}
