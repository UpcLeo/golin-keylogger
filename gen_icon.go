//go:build ignore

// 生成一个极简但合法的 icon.ico(48x48 PNG 内嵌),供资源编译使用。
// 运行: go run gen_icon.go
package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"os"
)

func main() {
	const size = 48
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	// 蓝色圆角矩形底 + 浅色内框,朴素系统工具风格
	bg := color.RGBA{0x2e, 0x6d, 0xb4, 0xff}
	fg := color.RGBA{0xd8, 0xe8, 0xf8, 0xff}
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			corner := (x < 8 && y < 8) || (x >= size-8 && y < 8) ||
				(x < 8 && y >= size-8) || (x >= size-8 && y >= size-8)
			if corner {
				continue // 圆角透明
			}
			img.Set(x, y, bg)
		}
	}
	draw.Draw(img, image.Rect(12, 20, 36, 24), &image.Uniform{fg}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(12, 28, 36, 30), &image.Uniform{fg}, image.Point{}, draw.Src)

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, img); err != nil {
		panic(err)
	}
	pngData := pngBuf.Bytes()

	// ICO 头: reserved(2) + type=1(2) + count=1(2)
	out := new(bytes.Buffer)
	binary.Write(out, binary.LittleEndian, uint16(0))
	binary.Write(out, binary.LittleEndian, uint16(1))
	binary.Write(out, binary.LittleEndian, uint16(1))
	// 目录项: w,h,colors,reserved,planes,bpp,size,offset
	out.WriteByte(byte(size))
	out.WriteByte(byte(size))
	out.WriteByte(0)
	out.WriteByte(0)
	binary.Write(out, binary.LittleEndian, uint16(1))
	binary.Write(out, binary.LittleEndian, uint16(32))
	binary.Write(out, binary.LittleEndian, uint32(len(pngData)))
	binary.Write(out, binary.LittleEndian, uint32(6+16))
	out.Write(pngData)

	if err := os.WriteFile("icon.ico", out.Bytes(), 0644); err != nil {
		panic(err)
	}
}
