//go:build linux

package term

import (
	"syscall"
)

// ioctl constants for Linux
const (
	tcgets = syscall.TCGETS
	tcsets = syscall.TCSETS
)
