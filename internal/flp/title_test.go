package flp

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func mini() []byte {
	var body bytes.Buffer
	ver := []byte("24.1.1.4234\x00")
	body.WriteByte(199)
	body.WriteByte(byte(len(ver)))
	body.Write(ver)
	body.WriteByte(159)
	binary.Write(&body, binary.LittleEndian, uint32(4234))
	body.WriteByte(64)
	body.Write([]byte{0, 0})
	var out bytes.Buffer
	out.WriteString("FLhd")
	binary.Write(&out, binary.LittleEndian, uint32(6))
	binary.Write(&out, binary.LittleEndian, int16(0))
	binary.Write(&out, binary.LittleEndian, uint16(1))
	binary.Write(&out, binary.LittleEndian, uint16(96))
	out.WriteString("FLdt")
	binary.Write(&out, binary.LittleEndian, uint32(body.Len()))
	out.Write(body.Bytes())
	return out.Bytes()
}

func TestTitle(t *testing.T) {
	f, err := Parse(mini())
	if err != nil {
		t.Fatal(err)
	}
	if f.Title() != "" || f.Build() != 4234 || f.Version() != "24.1.1.4234" {
		t.Fatalf("исходный файл: %q %d %q", f.Title(), f.Build(), f.Version())
	}
	if !f.SetTitle("Ночной дрифт") {
		t.Fatal("должен измениться")
	}
	g, err := Parse(f.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if g.Title() != "Ночной дрифт" || g.Events[2].ID != 194 || g.Events[3].ID != 64 {
		t.Fatalf("после записи: %q, события %v", g.Title(), []byte{g.Events[0].ID, g.Events[1].ID, g.Events[2].ID})
	}
	if g.SetTitle("Ночной дрифт") {
		t.Fatal("то же название — без изменений")
	}
	g.SetTitle("Другое")
	h, _ := Parse(g.Bytes())
	if h.Title() != "Другое" || len(h.Events) != 4 {
		t.Fatalf("замена: %q %d", h.Title(), len(h.Events))
	}
}
