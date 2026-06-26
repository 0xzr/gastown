//go:build linux || darwin || freebsd

package refinery

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func openStableRegularFile(path, label string) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("checking %s %s: %w", label, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, nil, fmt.Errorf("%s %s must not be a symlink", label, path)
	}
	if !info.Mode().IsRegular() {
		return nil, nil, fmt.Errorf("%s %s is not a regular file", label, path)
	}

	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ELOOP) {
			return nil, nil, fmt.Errorf("%s %s must not be a symlink", label, path)
		}
		return nil, nil, fmt.Errorf("opening %s %s: %w", label, path, err)
	}
	file := os.NewFile(uintptr(fd), path)
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("checking opened %s %s: %w", label, path, err)
	}
	if !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%s %s changed while opening", label, path)
	}
	if !openedInfo.Mode().IsRegular() {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%s %s is not a regular file", label, path)
	}
	return file, openedInfo, nil
}
