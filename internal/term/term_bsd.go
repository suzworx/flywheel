//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package term

import (
	"syscall"
)

// ioctl constants for BSD
const (
	tcgets = syscall.TIOCGETA
	tcsets = syscall.TIOCSETA
)
