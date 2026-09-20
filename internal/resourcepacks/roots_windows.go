//go:build windows

package resourcepacks

import (
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	driveRemovable = 2
	driveFixed     = 3
)

var (
	kernel32         = windows.NewLazySystemDLL("kernel32.dll")
	getLogicalDrives = kernel32.NewProc("GetLogicalDrives")
	getDriveType     = kernel32.NewProc("GetDriveTypeW")
)

func automaticRoots() []string {
	mask, _, _ := getLogicalDrives.Call()
	if mask == 0 {
		return nil
	}
	result := make([]string, 0, 8)
	for index := 0; index < 26; index++ {
		if mask&(1<<index) == 0 {
			continue
		}
		root := fmt.Sprintf("%c:\\", 'A'+index)
		pointer, err := syscall.UTF16PtrFromString(root)
		if err != nil {
			continue
		}
		typeValue, _, _ := getDriveType.Call(uintptr(unsafe.Pointer(pointer)))
		if typeValue == driveFixed || typeValue == driveRemovable {
			result = append(result, root)
		}
	}
	return result
}
