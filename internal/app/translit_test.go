package app

import "testing"

func TestToLatin(t *testing.T) {
	for in, want := range map[string]string{
		"Кирюха":            "Kiryukha",
		"Зима — демо 2":     "Zima — demo 2",
		"ЩУКА":              "SHCHUKA",
		"Щука":              "Shchuka",
		"Подъезд, ёлка":     "Podezd, yolka",
		"Lil Узбек feat. Я": "Lil Uzbek feat. Ya",
		"already latin":     "already latin",
	} {
		if got := toLatin(in); got != want {
			t.Errorf("toLatin(%q) = %q, хочу %q", in, got, want)
		}
	}
}
