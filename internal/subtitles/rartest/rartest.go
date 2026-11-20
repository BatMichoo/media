// Package rartest builds small RAR archives for tests.
package rartest

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
)

// Of makes a RAR5 archive (files stored, not compressed) of
// name/content pairs, the way WinRAR's "store" method writes them; a name
// ending in "/" is a folder. Go has no RAR writer, so it's built here by
// the format's description (rarlab.com/technote.htm) and checked against
// unrar in the subtitles package's TestRarOfIsReal.
func Of(files ...string) []byte { return build(0, files...) }

// Split is Of with every file marked as continuing in the next
// volume (a .part1.rar of a split archive).
func Split(files ...string) []byte { return build(0x10, files...) }

func build(extraHeaderFlags uint64, files ...string) []byte {
	vint := func(n uint64) []byte { return binary.AppendUvarint(nil, n) }
	block := func(fields ...[]byte) []byte {
		body := bytes.Join(fields, nil)
		h := append(vint(uint64(len(body))), body...)
		return append(binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(h)), h...)
	}
	var b bytes.Buffer
	b.Write([]byte("Rar!\x1a\x07\x01\x00"))
	archiveFlags := uint64(0)
	if extraHeaderFlags&0x10 != 0 {
		archiveFlags = 0x1 // a volume
	}
	b.Write(block(vint(1), vint(0), vint(archiveFlags))) // main header
	for i := 0; i < len(files); i += 2 {
		name, data := files[i], []byte(files[i+1])
		dir := len(name) > 0 && name[len(name)-1] == '/'
		fileFlags := uint64(0x4) // data CRC32 present
		if dir {
			name, data, fileFlags = name[:len(name)-1], nil, 0x1
		}
		fields := [][]byte{vint(2), vint(0x2 | extraHeaderFlags), vint(uint64(len(data))), // file header, data area
			vint(fileFlags), vint(uint64(len(data))), vint(0x20)} // flags, unpacked size, attributes
		if !dir {
			fields = append(fields, binary.LittleEndian.AppendUint32(nil, crc32.ChecksumIEEE(data)))
		}
		fields = append(fields, vint(0), vint(0), vint(uint64(len(name))), []byte(name)) // stored, Windows, name
		b.Write(block(fields...))
		b.Write(data)
	}
	b.Write(block(vint(5), vint(0), vint(0))) // end of archive
	return b.Bytes()
}
