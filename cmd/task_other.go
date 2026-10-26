//go:build !windows

package cmd

import "os/exec"

// hideConsole 非 Windows 平台无需隐藏控制台
func hideConsole(c *exec.Cmd) {}
