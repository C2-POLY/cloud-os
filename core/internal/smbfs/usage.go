package smbfs

import "github.com/C2-POLY/cloud-os/core/internal/storage"

// diskUsage returns the size and the free bytes of the disk dir is on
// (storage.DiskUsage). An unknown size still has to look like a usable drive
// to the Finder.
func diskUsage(dir string) (total, free uint64) {
	total, free, err := storage.DiskUsage(dir)
	if err != nil || total == 0 {
		return 1 << 42, 1 << 41
	}
	return total, free
}
