package flp

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"testing"
)

func TestParsePreservesEventStream(t *testing.T) {
	for _, version := range []string{"11.4.0", "11.5.0", "24.1.1", "26.1.3"} {
		t.Run(version, func(t *testing.T) {
			want := &File{Format: 1, Channels: 7, PPQ: 96}
			want.Events = append(want.Events, Event{ID: evFLVersion, Data: []byte(version + "\x00")})
			for i := 0; i < 10000; i++ {
				want.Events = append(want.Events,
					Event{ID: 0, Data: []byte{byte(i)}},
					Event{ID: 65, Data: []byte{byte(i), byte(i >> 8)}},
					Event{ID: 128, Data: []byte{1, 2, 3, 4}},
				)
			}
			data172 := []byte{1, 2, 3, 4}
			if version == "26.1.3" {
				data172 = []byte{1}
			}
			want.Events = append(want.Events, Event{ID: 172, Data: data172}, Event{ID: 213, Data: bytes.Repeat([]byte{0xab}, 1<<20)})
			raw := want.Bytes()
			got, err := Parse(raw)
			if err != nil {
				t.Fatal(err)
			}
			if got.Format != want.Format || got.Channels != want.Channels || got.PPQ != want.PPQ || !reflect.DeepEqual(got.Events, want.Events) {
				t.Fatal("event order, header or payload changed")
			}
			if !bytes.Equal(got.Bytes(), raw) {
				t.Fatal("rewriting an unchanged parsed file changed its bytes")
			}
			if got.utf16 != (version != "11.4.0") {
				t.Fatal("text encoding changed")
			}
			// Parsing must continue to reference the original payload, not copy
			// large plugin states when reserving the event slice.
			raw[len(raw)-1] = 0x7a
			last := got.Events[len(got.Events)-1].Data
			if last[len(last)-1] != 0x7a {
				t.Fatal("plugin payload was copied")
			}
		})
	}
}

func TestParseEmptyAndInvalidStream(t *testing.T) {
	empty := (&File{PPQ: 96}).Bytes()
	f, err := Parse(empty)
	if err != nil || f.Events != nil {
		t.Fatalf("empty stream: %v, %v", f, err)
	}
	valid := (&File{Events: []Event{{ID: 213, Data: bytes.Repeat([]byte{1}, 200)}}}).Bytes()
	invalidVarint := append(append([]byte{}, empty...), 213, 0x80)
	binary.LittleEndian.PutUint32(invalidVarint[18:22], 2)
	for _, raw := range [][]byte{nil, empty[:21], valid[:len(valid)-1], invalidVarint} {
		walkErr := walk(raw, nil, func(byte, []byte) bool { return true })
		got, err := Parse(raw)
		if walkErr == nil || err == nil || got != nil || err.Error() != walkErr.Error() {
			t.Fatalf("invalid input: Parse=%v/%v walk=%v", got, err, walkErr)
		}
	}
	// A declared empty FLdt ignores trailing bytes, as before.
	trailing := append(append([]byte{}, empty...), 213, 0x80)
	binary.LittleEndian.PutUint32(trailing[18:22], 0)
	if f, err := Parse(trailing); err != nil || f.Events != nil {
		t.Fatalf("trailing bytes: %v, %v", f, err)
	}
}
