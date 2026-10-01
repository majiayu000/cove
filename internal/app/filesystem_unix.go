//go:build !windows

package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func protectAppPath(path string, mode os.FileMode) error { return os.Chmod(path, mode) }
func replaceAppFile(from, to string) error               { return os.Rename(from, to) }
func syncAppDirectory(file *os.File) error               { return file.Sync() }

func resourceFreeSpace(ctx context.Context, dir string) (uint64, error) {
	probe, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(probe, "df", "-Pk", dir).Output()
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, errors.New("disk capacity unavailable")
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) < 4 {
		return 0, errors.New("disk capacity unavailable")
	}
	available, err := strconv.ParseUint(fields[3], 10, 64)
	return available * 1024, err
}

func appDirectoryIdentity(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("not a directory")
	}
	stat := info.Sys().(*syscall.Stat_t)
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

// OpenFile already creates Unix staging files with mode 0600.
func protectAppFile(*os.File) error { return nil }
