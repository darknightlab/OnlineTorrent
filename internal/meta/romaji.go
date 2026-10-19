package meta

import (
	"strings"
	"unicode"
)

// Offline kana → romaji (Hepburn-ish) for Japanese titles without a romanised alias, so a
// fansub title like "Tanuki to Kitsune" can find タヌキとキツネ. Kanji can't be read
// offline: they end a word (the result only carries the kana parts).

var kanaRomaji = map[rune]string{
	'あ': "a", 'い': "i", 'う': "u", 'え': "e", 'お': "o",
	'か': "ka", 'き': "ki", 'く': "ku", 'け': "ke", 'こ': "ko",
	'さ': "sa", 'し': "shi", 'す': "su", 'せ': "se", 'そ': "so",
	'た': "ta", 'ち': "chi", 'つ': "tsu", 'て': "te", 'と': "to",
	'な': "na", 'に': "ni", 'ぬ': "nu", 'ね': "ne", 'の': "no",
	'は': "ha", 'ひ': "hi", 'ふ': "fu", 'へ': "he", 'ほ': "ho",
	'ま': "ma", 'み': "mi", 'む': "mu", 'め': "me", 'も': "mo",
	'や': "ya", 'ゆ': "yu", 'よ': "yo",
	'ら': "ra", 'り': "ri", 'る': "ru", 'れ': "re", 'ろ': "ro",
	'わ': "wa", 'ゐ': "i", 'ゑ': "e", 'を': "wo", 'ん': "n",
	'が': "ga", 'ぎ': "gi", 'ぐ': "gu", 'げ': "ge", 'ご': "go",
	'ざ': "za", 'じ': "ji", 'ず': "zu", 'ぜ': "ze", 'ぞ': "zo",
	'だ': "da", 'ぢ': "ji", 'づ': "zu", 'で': "de", 'ど': "do",
	'ば': "ba", 'び': "bi", 'ぶ': "bu", 'べ': "be", 'ぼ': "bo",
	'ぱ': "pa", 'ぴ': "pi", 'ぷ': "pu", 'ぺ': "pe", 'ぽ': "po",
	'ぁ': "a", 'ぃ': "i", 'ぅ': "u", 'ぇ': "e", 'ぉ': "o", 'ゔ': "vu",
}

// small ya/yu/yo after an -i kana: きゃ → kya, しゃ → sha
var smallY = map[rune]string{'ゃ': "a", 'ゅ': "u", 'ょ': "o"}

func toHiragana(r rune) rune {
	if r >= 'ァ' && r <= 'ヶ' {
		return r - 0x60
	}
	return r
}

func isKana(r rune) bool {
	h := toHiragana(r)
	return (h >= 'ぁ' && h <= 'ゖ') || r == 'ー' || r == 'ッ' || r == 'っ'
}

// Romanize transliterates the kana of s; other letters (latin) are kept, kanji and
// punctuation separate words. ok=false when s has no kana at all.
func Romanize(s string) (string, bool) {
	rs := []rune(normWidth(s))
	var b strings.Builder
	sawKana := false
	space := func() {
		if b.Len() > 0 && !strings.HasSuffix(b.String(), " ") {
			b.WriteByte(' ')
		}
	}
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case r == 'っ' || r == 'ッ':
			// doubles the next consonant
			if i+1 < len(rs) {
				if next := kanaRomaji[toHiragana(rs[i+1])]; next != "" && next[0] != 'a' && next[0] != 'i' && next[0] != 'u' && next[0] != 'e' && next[0] != 'o' {
					if strings.HasPrefix(next, "ch") {
						b.WriteByte('t')
					} else {
						b.WriteByte(next[0])
					}
				}
			}
			sawKana = true
		case r == 'ー':
			// long vowel: dropped (matching folds long vowels anyway)
			sawKana = true
		case isKana(r):
			sawKana = true
			h := toHiragana(r)
			ro := kanaRomaji[h]
			if i+1 < len(rs) {
				if y, ok := smallY[toHiragana(rs[i+1])]; ok && strings.HasSuffix(ro, "i") {
					base := strings.TrimSuffix(ro, "i")
					switch base {
					case "sh", "ch", "j":
						ro = base + y
					default:
						ro = base + "y" + y
					}
					i++
				}
			}
			b.WriteString(ro)
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(unicode.ToLower(r))
		default:
			space()
		}
	}
	return strings.TrimSpace(b.String()), sawKana
}
