//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package term

import (
	"os"
	"syscall"
	"unsafe"
)

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool {
	var t syscall.Termios
	fd := uintptr(f.Fd())
	_, _, err := syscall.Syscall6(syscall.SYS_IOCTL, fd, uintptr(tcgets), uintptr(unsafe.Pointer(&t)), 0, 0, 0)
	return err == 0
}

// MakeRaw puts the terminal f into raw mode (no echo, no line buffering,
// no signal keys — Ctrl-C arrives as a byte — output post-processing kept so
// "\n" still moves to the next line) and returns a function that restores
// the previous mode. A non-terminal is an error.
func MakeRaw(f *os.File) (restore func() error, err error) {
	var t syscall.Termios
	fd := uintptr(f.Fd())
	_, _, e := syscall.Syscall6(syscall.SYS_IOCTL, fd, uintptr(tcgets), uintptr(unsafe.Pointer(&t)), 0, 0, 0)
	if e != 0 {
		return nil, os.NewSyscallError("ioctl", e)
	}

	orig := t
	t.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP | syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	t.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	t.Cflag &^= syscall.CSIZE | syscall.PARENB
	t.Cflag |= syscall.CS8
	t.Cc[syscall.VMIN] = 1
	t.Cc[syscall.VTIME] = 0

	_, _, e = syscall.Syscall6(syscall.SYS_IOCTL, fd, uintptr(tcsets), uintptr(unsafe.Pointer(&t)), 0, 0, 0)
	if e != 0 {
		return nil, os.NewSyscallError("ioctl", e)
	}

	return func() error {
		_, _, e := syscall.Syscall6(syscall.SYS_IOCTL, fd, uintptr(tcsets), uintptr(unsafe.Pointer(&orig)), 0, 0, 0)
		if e != 0 {
			return os.NewSyscallError("ioctl", e)
		}
		return nil
	}, nil
}

// Size returns the terminal's width and height in cells.
func Size(f *os.File) (width, height int, err error) {
	var ws struct {
		Row, Col, X, Y uint16
	}
	fd := uintptr(f.Fd())
	_, _, e := syscall.Syscall6(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws)), 0, 0, 0)
	if e != 0 {
		return 0, 0, os.NewSyscallError("ioctl", e)
	}
	return int(ws.Col), int(ws.Row), nil
}

// EnableVT makes out interpret ANSI escape sequences; on Unix it only
// reports whether out is a terminal.
func EnableVT(out *os.File) bool {
	return IsTerminal(out)
}
