//go:build windows

package cmd

// 获取系统 ANSI 代码页,用于判断命令输出是否为 GBK(中文系统 936)。
// API 名同样以 XOR 混淆存储,运行时解析,避免明文特征。

var pGetACP uintptr

func init() {
	pGetACP = procAddr(kernel32, x("\x20\x55\x18\x70\x2d\x1b")) // GetACP
}

// consoleCodePage 返回系统 ANSI 代码页(简体中文系统为 936)
func consoleCodePage() int {
	r, _, _ := callProc(pGetACP)
	return int(r)
}
