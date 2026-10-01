//go:build darwin || linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func protectPlatformPath(path string, mode os.FileMode) error { return os.Chmod(path, mode) }

func lockDataDir(dir string) (*os.File, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("数据目录必须是当前用户的私有真实目录（0700），不会覆盖目录权限")
	}
	path := filepath.Join(dir, ".gatt.lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, fmt.Errorf("数据目录锁不是普通文件")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("无法打开数据目录锁: %w", err)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("数据目录已被其他实例使用或无法锁定: %w", err)
	}
	return f, nil
}

func syncPlatformDirectory(dir string) error {
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func replacePlatformFile(from, to string) error {
	if err := os.Rename(from, to); err != nil {
		return err
	}
	return syncPlatformDirectory(filepath.Dir(to))
}
