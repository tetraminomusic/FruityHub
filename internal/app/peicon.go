package app

import (
	"debug/pe"
	"encoding/binary"
	"errors"
)

// exeIconGroup читает из exe-файла (как файла, без загрузчика Windows) содержимое набора иконок id.
// Через LoadLibraryEx так нельзя: для пути запущенного exe Windows отдаёт уже загруженный модуль,
// то есть старые иконки, даже когда файл на диске уже заменён.
func exeIconGroup(path string, id uint32) ([]byte, error) {
	f, err := pe.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	s := f.Section(".rsrc")
	if s == nil {
		return nil, errors.New("в exe нет иконок")
	}
	d, err := s.Data()
	if err != nil {
		return nil, err
	}
	u16 := func(o uint32) uint32 {
		if int(o)+2 > len(d) {
			return 0
		}
		return uint32(binary.LittleEndian.Uint16(d[o:]))
	}
	u32 := func(o uint32) uint32 {
		if int(o)+4 > len(d) {
			return 0
		}
		return binary.LittleEndian.Uint32(d[o:])
	}
	// каталог: тип → id → язык (первый) → данные
	find := func(dir, want uint32, any bool) (uint32, bool) {
		n := u16(dir+12) + u16(dir+14)
		for i := uint32(0); i < n; i++ {
			e := dir + 16 + i*8
			if any || u32(e) == want {
				return u32(e + 4), true
			}
		}
		return 0, false
	}
	t, ok := find(0, rtGroupIconID, false)
	if !ok || t&0x80000000 == 0 {
		return nil, errors.New("в exe нет иконок")
	}
	g, ok := find(t&0x7fffffff, id, false)
	if !ok || g&0x80000000 == 0 {
		return nil, errors.New("в этой версии нет второй иконки")
	}
	l, ok := find(g&0x7fffffff, 0, true)
	if !ok || l&0x80000000 != 0 {
		return nil, errors.New("не прочитал иконку")
	}
	rva, size := u32(l), u32(l+4)
	off := rva - s.VirtualAddress
	if rva < s.VirtualAddress || int(off)+int(size) > len(d) || size == 0 {
		return nil, errors.New("не прочитал иконку")
	}
	return append([]byte(nil), d[off:off+size]...), nil
}

const rtGroupIconID = 14
