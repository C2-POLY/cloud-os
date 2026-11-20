package smbfs

import "golang.org/x/sys/unix"

// diskUsage returns the size and the free bytes of the disk dir is on. An
// unknown size still has to look like a usable drive to the Finder.
func diskUsage(dir string) (total, free uint64) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil || st.Blocks == 0 {
		return 1 << 42, 1 << 41
	}
	bs := uint64(st.Bsize)
	return uint64(st.Blocks) * bs, uint64(st.Bavail) * bs
}
