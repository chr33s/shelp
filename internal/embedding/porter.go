package embedding

import "strings"

// Stem applies the Porter algorithm in its C revision (bli->ble, logi->log),
// matching SQLite's FTS5 "porter" tokenizer so keyword and semantic search
// agree on word forms. Input must be lowercase ASCII; other words are returned unchanged.
func Stem(word string) string {
	if len(word) <= 2 {
		return word
	}
	for i := 0; i < len(word); i++ {
		if word[i] < 'a' || word[i] > 'z' {
			return word
		}
	}
	w := []byte(word)
	w = step1a(w)
	w = step1b(w)
	w = step1c(w)
	w = step2(w)
	w = step3(w)
	w = step4(w)
	w = step5(w)
	return string(w)
}

// consonant reports whether w[i] is a consonant; y is a consonant after a vowel
// position or at the start.
func consonant(w []byte, i int) bool {
	switch w[i] {
	case 'a', 'e', 'i', 'o', 'u':
		return false
	case 'y':
		return i == 0 || !consonant(w, i-1)
	}
	return true
}

// measure counts VC sequences in w, the "m" of the algorithm.
func measure(w []byte) int {
	n, i := 0, 0
	for i < len(w) && consonant(w, i) {
		i++
	}
	for i < len(w) {
		for i < len(w) && !consonant(w, i) {
			i++
		}
		if i >= len(w) {
			break
		}
		for i < len(w) && consonant(w, i) {
			i++
		}
		n++
	}
	return n
}

func hasVowel(w []byte) bool {
	for i := range w {
		if !consonant(w, i) {
			return true
		}
	}
	return false
}

func doubleConsonant(w []byte) bool {
	n := len(w)
	return n >= 2 && w[n-1] == w[n-2] && consonant(w, n-1)
}

// cvc reports a consonant-vowel-consonant ending whose last letter is not w, x or y.
func cvc(w []byte) bool {
	n := len(w)
	if n < 3 || !consonant(w, n-1) || consonant(w, n-2) || !consonant(w, n-3) {
		return false
	}
	switch w[n-1] {
	case 'w', 'x', 'y':
		return false
	}
	return true
}

func ends(w []byte, suffix string) bool {
	return len(w) >= len(suffix) && string(w[len(w)-len(suffix):]) == suffix
}

// replace swaps suffix for repl when the remaining stem has measure > min.
func replace(w []byte, suffix, repl string, min int) ([]byte, bool) {
	if !ends(w, suffix) {
		return w, false
	}
	stem := w[:len(w)-len(suffix)]
	if measure(stem) > min {
		return append(stem[:len(stem):len(stem)], repl...), true
	}
	return w, true
}

func step1a(w []byte) []byte {
	switch {
	case ends(w, "sses"), ends(w, "ies"):
		return w[:len(w)-2]
	case ends(w, "ss"):
		return w
	case ends(w, "s"):
		return w[:len(w)-1]
	}
	return w
}

func step1b(w []byte) []byte {
	if ends(w, "eed") {
		if measure(w[:len(w)-3]) > 0 {
			return w[:len(w)-1]
		}
		return w
	}
	var stem []byte
	switch {
	case ends(w, "ed") && hasVowel(w[:len(w)-2]):
		stem = w[:len(w)-2]
	case ends(w, "ing") && hasVowel(w[:len(w)-3]):
		stem = w[:len(w)-3]
	default:
		return w
	}
	stem = stem[:len(stem):len(stem)]
	switch {
	case ends(stem, "at"), ends(stem, "bl"), ends(stem, "iz"):
		return append(stem, 'e')
	case doubleConsonant(stem):
		switch stem[len(stem)-1] {
		case 'l', 's', 'z':
			return stem
		}
		return stem[:len(stem)-1]
	case measure(stem) == 1 && cvc(stem):
		return append(stem, 'e')
	}
	return stem
}

func step1c(w []byte) []byte {
	if ends(w, "y") && hasVowel(w[:len(w)-1]) {
		out := append([]byte{}, w...)
		out[len(out)-1] = 'i'
		return out
	}
	return w
}

var step2Rules = [][2]string{
	{"ational", "ate"}, {"tional", "tion"}, {"enci", "ence"}, {"anci", "ance"},
	{"izer", "ize"}, {"bli", "ble"}, {"alli", "al"}, {"entli", "ent"}, {"eli", "e"},
	{"ousli", "ous"}, {"ization", "ize"}, {"ation", "ate"}, {"ator", "ate"},
	{"alism", "al"}, {"iveness", "ive"}, {"fulness", "ful"}, {"ousness", "ous"},
	{"aliti", "al"}, {"iviti", "ive"}, {"biliti", "ble"}, {"logi", "log"},
}

var step3Rules = [][2]string{
	{"icate", "ic"}, {"ative", ""}, {"alize", "al"}, {"iciti", "ic"},
	{"ical", "ic"}, {"ful", ""}, {"ness", ""},
}

// applyRules uses the longest matching suffix, as the algorithm requires.
func applyRules(w []byte, rules [][2]string) []byte {
	best := -1
	for i, r := range rules {
		if ends(w, r[0]) && (best < 0 || len(r[0]) > len(rules[best][0])) {
			best = i
		}
	}
	if best < 0 {
		return w
	}
	out, _ := replace(w, rules[best][0], rules[best][1], 0)
	return out
}

func step2(w []byte) []byte { return applyRules(w, step2Rules) }
func step3(w []byte) []byte { return applyRules(w, step3Rules) }

var step4Suffixes = strings.Fields("al ance ence er ic able ible ant ement ment ent ion ou ism ate iti ous ive ize")

func step4(w []byte) []byte {
	best := ""
	for _, s := range step4Suffixes {
		if ends(w, s) && len(s) > len(best) {
			best = s
		}
	}
	if best == "" {
		return w
	}
	stem := w[:len(w)-len(best)]
	if best == "ion" && (len(stem) == 0 || (stem[len(stem)-1] != 's' && stem[len(stem)-1] != 't')) {
		return w
	}
	if measure(stem) > 1 {
		return stem
	}
	return w
}

func step5(w []byte) []byte {
	if ends(w, "e") {
		stem := w[:len(w)-1]
		if m := measure(stem); m > 1 || (m == 1 && !cvc(stem)) {
			w = stem
		}
	}
	if measure(w) > 1 && doubleConsonant(w) && w[len(w)-1] == 'l' {
		w = w[:len(w)-1]
	}
	return w
}
