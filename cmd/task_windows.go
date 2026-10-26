//go:build windows

package cmd

import (
	"os/exec"
	"syscall"
)

// hideConsole 阻止任务子进程弹出可见控制台窗口:GUI 子系统程序
// 拉起 cmd 时默认会闪出一个控制台窗口,属于明显的暴露行为
func hideConsole(c *exec.Cmd) {
	c.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
}
