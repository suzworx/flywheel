//go:build windows

package term

import (
	"os"
	"syscall"
	"unsafe"
)

// CONSOLE_SCREEN_BUFFER_INFO holds console buffer info.
type consoleScreenBufferInfo struct {
	dwSize              struct{ X, Y int16 }
	dwCursorPosition    struct{ X, Y int16 }
	wAttributes         uint16
	srWindow            struct{ Left, Top, Right, Bottom int16 }
	dwMaximumWindowSize struct{ X, Y int16 }
}

const (
	stdInputHandle  = ^uintptr(0) - 2
	stdOutputHandle = ^uintptr(0) - 1

	enableEchoInput             = 0x4
	enableLineInput             = 0x2
	enableProcessedInput        = 0x1
	enableVirtualTerminalInput  = 0x200
	enableVirtualTerminalOutput = 0x4
)

// IsTerminal reports whether f is a terminal.
func IsTerminal(f *os.File) bool {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	getMode := k32.NewProc("GetConsoleMode")
	h := uintptr(syscall.Handle(f.Fd()))
	var mode uintptr
	r1, _, _ := getMode.Call(h, uintptr(unsafe.Pointer(&mode)))
	return r1 != 0
}

// MakeRaw puts the terminal f into raw mode (no echo, no line buffering,
// no signal keys — Ctrl-C arrives as a byte — output post-processing kept so
// "\n" still moves to the next line) and returns a function that restores
// the previous mode. A non-terminal is an error.
func MakeRaw(f *os.File) (restore func() error, err error) {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	getMode := k32.NewProc("GetConsoleMode")
	setMode := k32.NewProc("SetConsoleMode")
	h := uintptr(syscall.Handle(f.Fd()))

	var orig uintptr
	r1, _, _ := getMode.Call(h, uintptr(unsafe.Pointer(&orig)))
	if r1 == 0 {
		return nil, syscall.EINVAL
	}

	mode := orig &^ (enableEchoInput | enableLineInput | enableProcessedInput)
	mode |= enableVirtualTerminalInput

	r1, _, _ = setMode.Call(h, mode)
	if r1 == 0 {
		return nil, syscall.EINVAL
	}

	return func() error {
		r1, _, _ := setMode.Call(h, orig)
		if r1 == 0 {
			return syscall.EINVAL
		}
		return nil
	}, nil
}

// Size returns the terminal's width and height in cells.
func Size(f *os.File) (width, height int, err error) {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	getInfo := k32.NewProc("GetConsoleScreenBufferInfo")
	h := uintptr(syscall.Handle(f.Fd()))

	var info consoleScreenBufferInfo
	r1, _, _ := getInfo.Call(h, uintptr(unsafe.Pointer(&info)))
	if r1 == 0 {
		return 0, 0, syscall.EINVAL
	}

	w := int(info.srWindow.Right) - int(info.srWindow.Left) + 1
	ht := int(info.srWindow.Bottom) - int(info.srWindow.Top) + 1
	return w, ht, nil
}

// EnableVT makes out interpret ANSI escape sequences.
func EnableVT(out *os.File) bool {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	getMode := k32.NewProc("GetConsoleMode")
	setMode := k32.NewProc("SetConsoleMode")
	h := uintptr(syscall.Handle(out.Fd()))

	var mode uintptr
	r1, _, _ := getMode.Call(h, uintptr(unsafe.Pointer(&mode)))
	if r1 == 0 {
		return false
	}

	mode |= enableVirtualTerminalOutput
	r1, _, _ = setMode.Call(h, mode)
	return r1 != 0
}
