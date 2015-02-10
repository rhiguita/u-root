package pty

import (
	"os"
	"strconv"
	"syscall"
	"unsafe"
)

func open() (*os.File, *os.File, string, error) {
	ptm, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, nil, "", err
	}

	sname, err := ptsname(ptm)
	if err != nil {
		return nil, nil, "", err
	}

	err = unlockpt(ptm)
	if err != nil {
		return nil, nil, "", err
	}

	pts, err := os.OpenFile(sname, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, "", err
	}
	return ptm, pts, sname, nil
}

func ptsname(f *os.File) (string, error) {
	var n _C_uint
	err := ioctl(f.Fd(), syscall.TIOCGPTN, uintptr(unsafe.Pointer(&n)))
	if err != nil {
		return "", err
	}
	return "/dev/pts/" + strconv.Itoa(int(n)), nil
}

func unlockpt(f *os.File) error {
	var u _C_int
	// use TIOCSPTLCK with a zero valued arg to clear the slave pty lock
	return ioctl(f.Fd(), syscall.TIOCSPTLCK, uintptr(unsafe.Pointer(&u)))
}