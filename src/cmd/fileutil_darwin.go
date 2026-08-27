//go:build darwin

package main

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// attrCmnCrtime is ATTR_CMN_CRTIME from <sys/attr.h>.
// setattrlist(2) uses it to set a file's birth (creation) timestamp on HFS+/APFS.
const attrCmnCrtime uint32 = 0x00000200

// preserveTimestamps copies atime, mtime, and birth time from src to dst.
// Birth time is macOS-only and requires setattrlist(2).
// Returns an error if any timestamp operation fails; the caller decides severity.
func preserveTimestamps(src, dst string) error {
	var st syscall.Stat_t
	if err := syscall.Lstat(src, &st); err != nil {
		return err
	}

	times := []unix.Timespec{
		{Sec: st.Atimespec.Sec, Nsec: st.Atimespec.Nsec},
		{Sec: st.Mtimespec.Sec, Nsec: st.Mtimespec.Nsec},
	}
	if err := unix.UtimesNano(dst, times); err != nil {
		return err
	}

	al := unix.Attrlist{
		Bitmapcount: 5, // ATTR_BIT_MAP_COUNT
		Commonattr:  attrCmnCrtime,
	}
	crtime := unix.Timespec{Sec: st.Birthtimespec.Sec, Nsec: st.Birthtimespec.Nsec}
	buf := unsafe.Slice((*byte)(unsafe.Pointer(&crtime)), unsafe.Sizeof(crtime))
	return unix.Setattrlist(dst, &al, buf, 0)
}
