//go:build windows

package main

import "golang.org/x/sys/windows"

// initConsoleUTF8 将控制台输入输出代码页切换为 UTF-8:
// Go 进程向标准输出写入 UTF-8 字节,而中文 Windows 控制台默认按 GBK(936)
// 解释,导致服务端终端里的中文日志(命令下发/回传结果)乱码。
// 仅影响当前进程的控制台,不改系统设置。
func initConsoleUTF8() {
	_ = windows.SetConsoleOutputCP(65001) // CP_UTF8
	_ = windows.SetConsoleCP(65001)
}
