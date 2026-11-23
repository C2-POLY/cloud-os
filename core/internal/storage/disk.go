package storage

import "golang.org/x/sys/unix"

// DiskUsage returns the size and the free bytes of the file system dir is on.
// The free bytes are those a process without root's reserve can still write:
// what an upload, an SMB client or the box's status can count on.
func DiskUsage(dir string) (total, free uint64, err error) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return 0, 0, err
	}
	bs := uint64(st.Bsize) //nolint:gosec // a block size is positive
	return st.Blocks * bs, st.Bavail * bs, nil
}
