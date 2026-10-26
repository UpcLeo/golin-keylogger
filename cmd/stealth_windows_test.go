//go:build windows

package cmd

import "testing"

// TestStealthThresholdsNormalize 非法阈值(≤0)应回退默认值,防止误配置导致永不通过
func TestStealthThresholdsNormalize(t *testing.T) {
	old := [3]int{stealthUpMin, stealthMemGB, stealthCPUs}
	stealthUpMin, stealthMemGB, stealthCPUs = 0, -1, 0
	defer func() { stealthUpMin, stealthMemGB, stealthCPUs = old[0], old[1], old[2] }()

	u, m, c := stealthThresholds()
	if u != 10 || m != 4 || c != 2 {
		t.Fatalf("thresholds = %d/%d/%d, want 10/4/2", u, m, c)
	}

	stealthUpMin, stealthMemGB, stealthCPUs = 1, 2, 1
	if u2, m2, c2 := stealthThresholds(); u2 != 1 || m2 != 2 || c2 != 1 {
		t.Fatalf("thresholds = %d/%d/%d, want 1/2/1", u2, m2, c2)
	}
}

// TestStealthGateDisabled 未开启 --stealth 时直接放行,不执行任何检查
func TestStealthGateDisabled(t *testing.T) {
	old := stealthFlag
	stealthFlag = false
	defer func() { stealthFlag = old }()

	if !stealthGate() {
		t.Fatal("非 stealth 模式应直接放行")
	}
}

// TestStealthGateTimeout 延迟 0 秒时,检查未通过应立即判定失败
// (真实桌面环境即使环境检查通过,若无鼠标活动且在宽限期内,也应返回 false;
// 若在资源受限环境,环境检查本身不通过,同样返回 false。两种路径均确定)
func TestStealthGateTimeout(t *testing.T) {
	oldF, oldD := stealthFlag, stealthDelay
	stealthFlag, stealthDelay = true, 0
	defer func() {
		stealthFlag, stealthDelay = oldF, oldD
	}()

	if stealthGate() {
		t.Fatal("延迟 0 秒且无鼠标活动,应判定未通过")
	}
}

// TestStealthAPIs 直接调用底层检查 API,确认解析与结构体偏移无误
// (在真实机器上 uptime/ram/cpu 应为合理数值而非零)
func TestStealthAPIs(t *testing.T) {
	if sysUptimeMs() == 0 {
		t.Fatal("系统运行时间不应为 0")
	}
	if totalRAMBytes() < 512*1024*1024 {
		t.Fatalf("物理内存数值异常: %d", totalRAMBytes())
	}
	if cpuCount() == 0 {
		t.Fatal("逻辑处理器数量不应为 0")
	}
	cursorPos() // 仅验证调用不崩溃
}
