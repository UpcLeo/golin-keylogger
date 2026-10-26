package cmd

// 简单的 XOR 字符串混淆:避免敏感字符串以明文形式出现在二进制中,
// 降低静态特征匹配的命中率。密钥与编码字面量仍以常量形式存在,
// 目的仅是规避字符串签名,不构成强加密;如需更强混淆请配合 garble 构建。

var obfKey = []byte{0x67, 0x30, 0x6c, 0x31, 0x6e, 0x4b, 0x33, 0x79} // "g0l1nK3y"

// x 解码 XOR 混淆的字符串
func x(enc string) string {
	b := []byte(enc)
	for i := range b {
		b[i] ^= obfKey[i%len(obfKey)]
	}
	return string(b)
}
