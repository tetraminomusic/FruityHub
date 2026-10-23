package app

import (
	"strings"
	"unicode"
)

// FL Studio показывает кириллицу в Project info (Title, Author) кракозябрами, поэтому туда пишем латиницей:
// «Кирюха — Зима» → «Kiryukha — Zima». В FruityHub название остаётся как ввели.
var translitMap = map[rune]string{
	'а': "a", 'б': "b", 'в': "v", 'г': "g", 'д': "d", 'е': "e", 'ё': "yo", 'ж': "zh", 'з': "z", 'и': "i",
	'й': "y", 'к': "k", 'л': "l", 'м': "m", 'н': "n", 'о': "o", 'п': "p", 'р': "r", 'с': "s", 'т': "t",
	'у': "u", 'ф': "f", 'х': "kh", 'ц': "ts", 'ч': "ch", 'ш': "sh", 'щ': "shch", 'ъ': "", 'ы': "y", 'ь': "",
	'э': "e", 'ю': "yu", 'я': "ya",
	'і': "i", 'ї': "yi", 'є': "ye", 'ґ': "g", 'ў': "u",
}

// toLatin — транслитерация кириллицы; остальные символы — как есть.
func toLatin(s string) string {
	rs := []rune(s)
	var b strings.Builder
	for i, r := range rs {
		lo := unicode.ToLower(r)
		t, ok := translitMap[lo]
		if !ok {
			b.WriteRune(r)
			continue
		}
		if lo != r && t != "" { // заглавная: «Щ» → «Shch», а внутри слова из заглавных — «SHCH»
			allCaps := (i+1 < len(rs) && unicode.IsUpper(rs[i+1])) || (i > 0 && unicode.IsUpper(rs[i-1]) && (i+1 >= len(rs) || !unicode.IsLetter(rs[i+1])))
			if allCaps {
				t = strings.ToUpper(t)
			} else {
				t = strings.ToUpper(t[:1]) + t[1:]
			}
		}
		b.WriteString(t)
	}
	return b.String()
}

func hasCyrillic(s string) bool {
	for _, r := range s {
		if unicode.Is(unicode.Cyrillic, r) {
			return true
		}
	}
	return false
}
