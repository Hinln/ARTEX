//go:build windows

package evidence

import "os"

// syncDir is a no-op on Windows: FlushFileBuffers needs a handle opened for
// write, and a directory handle is read-only, so Sync() returns "Access is
// denied". Rename is atomic on NTFS already; the durability trick has no
// equivalent here.
func syncDir(*os.File) error { return nil }
