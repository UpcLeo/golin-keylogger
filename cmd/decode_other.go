//go:build !windows

package cmd

// decodeCmdOutput 非 Windows 平台的命令输出天然为 UTF-8,原样返回
func decodeCmdOutput(b []byte) string {
	return string(b)
}
