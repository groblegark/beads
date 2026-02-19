//go:build windows

package main

import (
	"golang.org/x/sys/windows"
	"unsafe"
)

// checkDiskSpace returns the available disk space in MB for the given path.
// Returns (availableMB, true) on success, (0, false) on failure.
func checkDiskSpace(path string) (uint64, bool) {
	var freeBytesAvailable, totalBytes, totalFreeBytes uint64

	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, false
	}

	err = windows.GetDiskFreeSpaceEx(
		pathPtr,
		(*uint64)(unsafe.Pointer(&freeBytesAvailable)), // SAFETY: stack-allocated uint64, valid alignment for Windows API
		(*uint64)(unsafe.Pointer(&totalBytes)),          // SAFETY: stack-allocated uint64, valid alignment for Windows API
		(*uint64)(unsafe.Pointer(&totalFreeBytes)),      // SAFETY: stack-allocated uint64, valid alignment for Windows API
	)
	if err != nil {
		return 0, false
	}

	// Convert to MB
	availableMB := freeBytesAvailable / (1024 * 1024)
	return availableMB, true
}
