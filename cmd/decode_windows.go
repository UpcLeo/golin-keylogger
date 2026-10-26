//go:build windows

package cmd

import (
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// consoleCodePageFn 获取系统 ANSI 代码页的函数,测试可替换
var consoleCodePageFn = consoleCodePage

// decodeCmdOutput 将 cmd 命令输出转为 UTF-8:
// 中文系统(代码页 936)下 cmd 内建命令与多数程序输出 GBK 字节,
// 直接按 UTF-8 回传会在服务端显示乱码,此处按系统代码页解码。
// 已是合法 UTF-8 的输出(如 chcp 65001 后的程序输出)原样保留。
func decodeCmdOutput(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	if consoleCodePageFn() == 936 { // 简体中文 GBK
		if s, err := simplifiedchinese.GBK.NewDecoder().Bytes(b); err == nil {
			return string(s)
		}
	}
	return string(b)
}
