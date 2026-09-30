//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"
)

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")
var setFileSecurity = syscall.NewLazyDLL("advapi32.dll").NewProc("SetFileSecurityW")
var convertSecurityDescriptor = syscall.NewLazyDLL("advapi32.dll").NewProc("ConvertStringSecurityDescriptorToSecurityDescriptorW")

func protectWindowsDirectory(path string) error {
	token, err := syscall.OpenCurrentProcessToken()
	if err != nil {
		return err
	}
	defer token.Close()
	owner, err := token.GetTokenUser()
	if err != nil {
		return err
	}
	sid, err := owner.User.Sid.String()
	if err != nil {
		return err
	}
	// Protected DACL: only this user receives full access; new children inherit it.
	text, err := syscall.UTF16PtrFromString("D:P(A;OICI;FA;;;" + sid + ")")
	if err != nil {
		return err
	}
	var descriptor uintptr
	ok, _, callErr := convertSecurityDescriptor.Call(uintptr(unsafe.Pointer(text)), 1, uintptr(unsafe.Pointer(&descriptor)), 0)
	if ok == 0 {
		return fmt.Errorf("无法建立当前用户 ACL: %w", callErr)
	}
	defer syscall.LocalFree(syscall.Handle(descriptor))
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	ok, _, callErr = setFileSecurity.Call(uintptr(unsafe.Pointer(name)), 0x80000004, descriptor)
	if ok == 0 {
		return fmt.Errorf("无法设置当前用户私有 ACL: %w", callErr)
	}
	return nil
}
func lockDataDir(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("数据目录必须是真实目录")
	}
	if err = protectWindowsDirectory(dir); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, ".gatt.lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("数据目录锁不是普通文件")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	var overlapped syscall.Overlapped
	ok, _, callErr := lockFileEx.Call(f.Fd(), 0x00000003, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		f.Close()
		return nil, fmt.Errorf("数据目录已被其他实例使用或无法锁定: %w", callErr)
	}
	return f, nil // Closing the handle releases the byte-range lock.
}

// File contents are flushed before rename. Native crash/power-loss testing is
// still required before claiming a production Windows package.
func syncPlatformDirectory(string) error { return nil }

var moveFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("MoveFileExW")

func replacePlatformFile(from, to string) error {
	source, err := syscall.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	target, err := syscall.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	ok, _, callErr := moveFileEx.Call(uintptr(unsafe.Pointer(source)), uintptr(unsafe.Pointer(target)), 0x00000009)
	if ok == 0 {
		return fmt.Errorf("Windows binary 仍被占用或替换失败: %w", callErr)
	}
	return nil
}
