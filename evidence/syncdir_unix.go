//go:build !windows

package evidence

import "os"

// syncDir flushes a directory entry. Only POSIX filesystems need (and allow) it.
func syncDir(d *os.File) error { return d.Sync() }
