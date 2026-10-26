//go:build windows

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// dbgLog 诊断日志:仅 --debug 时追加写 %TEMP%\golin_debug.log,
// 用于排查安装、互保与拉起链路(程序为 GUI 子系统,控制台不可见)。
// 正常部署不带 --debug,零开销。
var dbgMu sync.Mutex

func dbgLog(format string, a ...interface{}) {
	if !debugFlag {
		return
	}
	dbgMu.Lock()
	defer dbgMu.Unlock()
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "golin_debug.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s [pid=%d] %s\n",
		time.Now().Format("2006-01-02 15:04:05.000"), os.Getpid(),
		fmt.Sprintf(format, a...))
}
