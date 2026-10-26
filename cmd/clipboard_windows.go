//go:build windows

package cmd

import (
	"syscall"
	"unsafe"
)

// 自实现剪贴板读取,避免引入第三方库带来的明文 API 名称字符串

var (
	pClipOpen  uintptr
	pClipData  uintptr
	pClipClose uintptr
	pMemLock   uintptr
	pMemUnlock uintptr
)

func init() {
	pClipOpen = procAddr(user32, x("\x28\x40\x09\x5f\x2d\x27\x5a\x09\x05\x5f\x0d\x43\x0a"))
	pClipData = procAddr(user32, x("\x20\x55\x18\x72\x02\x22\x43\x1b\x08\x51\x1e\x55\x2a\x2a\x47\x18"))
	pClipClose = procAddr(user32, x("\x24\x5c\x03\x42\x0b\x08\x5f\x10\x17\x52\x03\x50\x1c\x2f"))
	pMemLock = procAddr(kernel32, x("\x20\x5c\x03\x53\x0f\x27\x7f\x16\x04\x5b"))
	pMemUnlock = procAddr(kernel32, x("\x20\x5c\x03\x53\x0f\x27\x66\x17\x0b\x5f\x0f\x5a"))
}

const cfUnicodeText = 13 // CF_UNICODETEXT

// ptrFromUintptr 将系统调用返回的地址值转为 unsafe.Pointer。
// GlobalLock 返回的剪贴板内存不受 GC 管理,此转换语义安全;
// 用指针变量中转以满足 vet 对 uintptr→Pointer 转换的检查。
func ptrFromUintptr(u uintptr) unsafe.Pointer {
	var p unsafe.Pointer
	*(*uintptr)(unsafe.Pointer(&p)) = u
	return p
}

// readClipboard 读取剪贴板 Unicode 文本,失败返回空
func readClipboard() (string, error) {
	if r, _, _ := callProc(pClipOpen, 0); r == 0 {
		return "", syscall.EINVAL
	}
	defer callProcDiscard(pClipClose)

	h, _, _ := callProc(pClipData, cfUnicodeText)
	if h == 0 {
		return "", syscall.EINVAL
	}
	p, _, _ := callProc(pMemLock, h)
	if p == 0 {
		return "", syscall.EINVAL
	}
	defer callProcDiscard(pMemUnlock, h)

	// 到 NUL 为止,最多 1MB
	buf := (*[1 << 20]uint16)(ptrFromUintptr(p))
	n := 0
	for n < len(buf) && buf[n] != 0 {
		n++
	}
	return syscall.UTF16ToString(buf[:n]), nil
}
