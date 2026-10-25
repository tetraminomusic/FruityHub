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

func TestArtist(t *testing.T) {
	f, _ := Parse(mini())
	if !f.SetTitle("Трек") || !f.SetArtist("Кирюха") {
		t.Fatal("должно измениться")
	}
	g, err := Parse(f.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if g.Artist() != "Кирюха" || g.Title() != "Трек" || g.Info().Artist != "Кирюха" {
		t.Fatalf("%q %q", g.Artist(), g.Title())
	}
	// артист после названия, остальное на месте
	if g.Events[2].ID != EvTitle || g.Events[3].ID != EvArtists || g.Events[4].ID != 64 {
		t.Fatalf("порядок: %v", []byte{g.Events[2].ID, g.Events[3].ID, g.Events[4].ID})
	}
}

func TestTempo(t *testing.T) {
	f, _ := Parse(mini())
	if f.Tempo() != 0 {
		t.Fatal("темпа нет — 0")
	}
	b := make([]byte, 4)
	binary.LittleEndian.PutUint32(b, 87500)
	f.Events = append([]Event{{ID: 156, Data: b}}, f.Events...)
	if f.Tempo() != 87.5 {
		t.Fatal(f.Tempo())
	}
	old, _ := Parse(mini())
	old.Events = append(old.Events, Event{ID: 66, Data: []byte{140, 0}})
	if old.Tempo() != 140 {
		t.Fatal(old.Tempo())
	}
}
