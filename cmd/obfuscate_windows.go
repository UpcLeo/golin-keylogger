//go:build windows

package cmd

import "syscall"

// 运行时加载 DLL 并解析导出函数,避免 API 名称明文出现在二进制中
// (字符串混淆见 obfuscate.go)

// 各模块共用的 DLL 句柄:集中在此加载,避免多文件重复 LoadLibrary
// 与重复维护同名 DLL 的混淆字面量。LoadLibrary 引用计数保证共享安全。
var (
	kernel32 = loadDLL(x("\x0c\x55\x1e\x5f\x0b\x27\x00\x4b\x49\x54\x00\x5d")) // kernel32.dll
	user32   = loadDLL(x("\x12\x43\x09\x43\x5d\x79\x1d\x1d\x0b\x5c"))         // user32.dll
	advapi32 = loadDLL(x("\x06\x54\x1a\x50\x1e\x22\x00\x4b\x49\x54\x00\x5d")) // advapi32.dll
	ole32    = loadDLL(x("\x08\x5c\x09\x02\x5c\x65\x57\x15\x0b"))             // ole32.dll
)

// loadDLL 运行时加载 DLL,返回句柄
func loadDLL(name string) syscall.Handle {
	h, err := syscall.LoadLibrary(name)
	if err != nil {
		panic("load dll failed")
	}
	return h
}

// procAddr 运行时解析导出函数地址
func procAddr(dll syscall.Handle, name string) uintptr {
	a, err := syscall.GetProcAddress(dll, name)
	if err != nil {
		panic("resolve proc failed")
	}
	return a
}

// callProc 通过函数地址调用 Windows API
func callProc(addr uintptr, args ...uintptr) (uintptr, uintptr, syscall.Errno) {
	return syscall.SyscallN(addr, args...)
}

// callProcDiscard 调用 Windows API 并显式丢弃返回值与错误码。
// 用于只关心副作用的调用(如 CloseHandle/RegCloseKey 等句柄释放),
// 显式忽略返回值以消除静态检查的"未处理错误"告警。
func callProcDiscard(addr uintptr, args ...uintptr) {
	_, _, _ = callProc(addr, args...)
}
