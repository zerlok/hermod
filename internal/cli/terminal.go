package cli

import (
	"os"
	"syscall"
	"unsafe"
)

// isTerminal reports whether f is a terminal: whether it has terminal
// attributes to read. A character device that is none, such as /dev/null,
// has none.
func isTerminal(f *os.File) bool {
	var attrs syscall.Termios
	// NOTE: the ioctl writes a Termios into attrs and keeps no reference to
	// it, so passing its address as a uintptr for the call's duration is safe.
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, f.Fd(), ioctlGetTermios, uintptr(unsafe.Pointer(&attrs)))
	return errno == 0
}
