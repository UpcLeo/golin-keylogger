//go:build windows

package cmd

import "testing"

// TestDecodeCmdOutputGBK 中文系统(936)下 GBK 输出应转为 UTF-8
func TestDecodeCmdOutputGBK(t *testing.T) {
	old := consoleCodePageFn
	consoleCodePageFn = func() int { return 936 }
	defer func() { consoleCodePageFn = old }()

	// "你好" 的 GBK 编码
	gbk := []byte{0xC4, 0xE3, 0xBA, 0xC3}
	if got := decodeCmdOutput(gbk); got != "你好" {
		t.Fatalf("decodeCmdOutput(gbk) = %q, want 你好", got)
	}
	// 已是 UTF-8 的输出原样保留
	if got := decodeCmdOutput([]byte("hello 世界")); got != "hello 世界" {
		t.Fatalf("decodeCmdOutput(utf8) = %q", got)
	}
	// 截断的 GBK 尾字节不 panic,以替换符占位
	if got := decodeCmdOutput([]byte{0xC4}); got != "�" {
		t.Fatalf("decodeCmdOutput(truncated) = %q, want 替换符", got)
	}
}

// TestDecodeCmdOutputNonGBK 非中文系统不转换,字节原样保留
func TestDecodeCmdOutputNonGBK(t *testing.T) {
	old := consoleCodePageFn
	consoleCodePageFn = func() int { return 437 }
	defer func() { consoleCodePageFn = old }()

	raw := []byte{0xC4, 0xE3}
	if got := decodeCmdOutput(raw); string(got) != string(raw) {
		t.Fatalf("decodeCmdOutput should pass through on non-936, got %q", got)
	}
}
