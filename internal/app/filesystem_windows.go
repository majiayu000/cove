//go:build windows

package app

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func protectAppPath(path string, mode os.FileMode) error {
	owner, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	inheritance := ""
	if mode == 0700 {
		inheritance = "OICI"
	}
	descriptor, err := windows.SecurityDescriptorFromString("D:P(A;" + inheritance + ";FA;;;" + owner.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}
func replaceAppFile(from, to string) error {
	source, err := windows.UTF16PtrFromString(from)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(to)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}

// Windows flushes file handles before publication and uses write-through moves;
// Unix directory fsync is unavailable. Power-loss acceptance remains separate.
func syncAppDirectory(*os.File) error { return nil }
func resourceFreeSpace(ctx context.Context, dir string) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	name, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, err
	}
	var available uint64
	if err := windows.GetDiskFreeSpaceEx(name, &available, nil, nil); err != nil {
		return 0, err
	}
	return available, ctx.Err()
}

func appDirectoryIdentity(path string) (result string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("not a directory")
	}
	var identity windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &identity); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d:%d", identity.VolumeSerialNumber, identity.FileIndexHigh, identity.FileIndexLow), nil
}

var reopenAppFile = windows.NewLazySystemDLL("kernel32.dll").NewProc("ReOpenFile")

func protectAppFile(file *os.File) (err error) {
	owner, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	const clientAccess = windows.STANDARD_RIGHTS_REQUIRED | windows.SYNCHRONIZE | (0x1ff &^ windows.FILE_EXECUTE)
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf("D:P(A;;0x%x;;;%s)", clientAccess, owner.User.Sid.String()))
	if err != nil {
		return err
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return err
	}
	// Go's writable file handle does not request WRITE_DAC. Reopen the same
	// object by handle, so ACL changes remain confined when parents are renamed.
	handle, _, callErr := reopenAppFile.Call(file.Fd(), windows.WRITE_DAC|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, 0)
	if windows.Handle(handle) == windows.InvalidHandle {
		return fmt.Errorf("reopen private file for ACL: %w", callErr)
	}
	defer func() { err = errors.Join(err, windows.CloseHandle(windows.Handle(handle))) }()
	if err = windows.SetSecurityInfo(windows.Handle(handle), windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		return fmt.Errorf("set private file ACL: %w", err)
	}
	return nil
}
