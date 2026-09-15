package process

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"unsafe"
)

const (
	defaultPTYRows = 24
	defaultPTYCols = 80
)

func setPipeProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func setTTYProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
}

func openPTY() (*os.File, *os.File, error) {
	masterFD, err := syscall.Open("/dev/ptmx", syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	masterOpen := true
	defer func() {
		if masterOpen {
			_ = syscall.Close(masterFD)
		}
	}()

	if err := ioctl(masterFD, syscall.TIOCPTYGRANT, 0); err != nil {
		return nil, nil, err
	}
	if err := ioctl(masterFD, syscall.TIOCPTYUNLK, 0); err != nil {
		return nil, nil, err
	}

	var name [128]byte
	if err := ioctlPtr(masterFD, syscall.TIOCPTYGNAME, unsafe.Pointer(&name[0])); err != nil {
		return nil, nil, err
	}
	nameEnd := bytes.IndexByte(name[:], 0)
	if nameEnd < 0 {
		return nil, nil, syscall.EINVAL
	}
	slaveName := string(name[:nameEnd])
	slaveFD, err := syscall.Open(slaveName, syscall.O_RDWR|syscall.O_NOCTTY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, nil, err
	}
	slaveOpen := true
	defer func() {
		if slaveOpen {
			_ = syscall.Close(slaveFD)
		}
	}()

	ws := winsize{Rows: defaultPTYRows, Cols: defaultPTYCols}
	if err := ioctlPtr(slaveFD, syscall.TIOCSWINSZ, unsafe.Pointer(&ws)); err != nil {
		return nil, nil, err
	}

	masterOpen = false
	slaveOpen = false
	return os.NewFile(uintptr(masterFD), "/dev/ptmx"), os.NewFile(uintptr(slaveFD), slaveName), nil
}

type winsize struct {
	Rows    uint16
	Cols    uint16
	XPixels uint16
	YPixels uint16
}

func ioctl(fd int, req uintptr, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), req, arg)
	if errno != 0 {
		return errno
	}
	return nil
}

func ioctlPtr(fd int, req uintptr, arg unsafe.Pointer) error {
	return ioctl(fd, req, uintptr(arg))
}

func signalProcessGroup(pid int, signal os.Signal) error {
	sig, ok := signal.(syscall.Signal)
	if !ok {
		return syscall.EINVAL
	}
	return syscall.Kill(-pid, sig)
}

func killProcessGroup(pid int) error {
	return syscall.Kill(-pid, syscall.SIGKILL)
}

func processGroupAlive(pid int) (bool, error) {
	err := syscall.Kill(-pid, 0)
	if isProcessDone(err) {
		return false, nil
	}
	// Darwin also returns EPERM for a group containing only zombies. Keep
	// checking until it disappears; EPERM alone never proves cleanup succeeded.
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return err == nil, err
}

func isProcessDone(err error) bool {
	return errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH)
}
