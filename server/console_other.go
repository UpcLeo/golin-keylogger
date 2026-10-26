//go:build !windows

package main

// initConsoleUTF8 非 Windows 平台终端天然为 UTF-8,无需处理
func initConsoleUTF8() {}
