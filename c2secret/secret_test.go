package c2secret

import (
	"testing"

	"golin/c2config"
)

// TestDecodeMatchesConfig 守护生成的密文与 c2config 包明文常量的一致性:
// 两端必须同值,否则客户端会被服务端拒绝或上报到错误的接口。
func TestDecodeMatchesConfig(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"ServerURL", ServerURL(), c2config.ServerURL},
		{"Token", Token(), c2config.Token},
		{"HeaderName", HeaderName(), c2config.HeaderName},
		{"ReportPath", ReportPath(), c2config.ReportPath},
		{"HealthPath", HealthPath(), c2config.HealthPath},
		{"TasksPath", TasksPath(), c2config.TasksPath},
		{"ResultPath", ResultPath(), c2config.ResultPath},
	}
	for _, c := range cases {
		if c.got == "" {
			t.Fatalf("%s 解密为空,请重新运行 go run ./tools/secretgen", c.name)
		}
		if c.got != c.want {
			t.Fatalf("%s = %q, want %q", c.name, c.got, c.want)
		}
	}
}

// TestAssembleKeyDistinct 密钥碎片与掩码不应完全相同(生成器自检)
func TestAssembleKeyDistinct(t *testing.T) {
	k := assembleKey()
	if len(k) != 32 {
		t.Fatalf("assembleKey len = %d, want 32", len(k))
	}
	allZero := true
	for _, b := range k {
		if b != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		t.Fatal("assembleKey 全零,secretdata.go 可能损坏")
	}
}
