//go:build windows

package refinery

import (
	"fmt"
	"os"
	"syscall"
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

	ptr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s %s: %w", label, path, err)
	}
	handle, err := syscall.CreateFile(
		ptr,
		syscall.GENERIC_READ,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil,
		syscall.OPEN_EXISTING,
		syscall.FILE_ATTRIBUTE_NORMAL|syscall.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s %s: %w", label, path, err)
	}
	file := os.NewFile(uintptr(handle), path)
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, fmt.Errorf("checking opened %s %s: %w", label, path, err)
	}
	if openedInfo.Mode()&os.ModeSymlink != 0 {
		_ = file.Close()
		return nil, nil, fmt.Errorf("%s %s must not be a symlink", label, path)
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
