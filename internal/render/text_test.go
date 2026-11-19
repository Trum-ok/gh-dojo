package render

import "testing"

func TestEditDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"", "by", 2},
		{"dryrun", "dry-run", 1},
		{"josn", "json", 2},
		{"статус", "статуc", 1}, // runes, not bytes
	} {
		if got := EditDistance(tc.a, tc.b); got != tc.want {
			t.Errorf("EditDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestJoinWords(t *testing.T) {
	for _, tc := range []struct {
		words []string
		want  string
	}{
		{nil, ""},
		{[]string{"a"}, "a"},
		{[]string{"a", "b"}, "a or b"},
		{[]string{"a", "b", "c"}, "a, b or c"},
	} {
		if got := JoinWords(tc.words, "or"); got != tc.want {
			t.Errorf("JoinWords(%q) = %q, want %q", tc.words, got, tc.want)
		}
	}
}

func TestCutPrefixFold(t *testing.T) {
	for _, tc := range []struct {
		s, prefix, rest string
		ok              bool
	}{
		{"Type:Bug", "type:", "Bug", true},
		{"type:", "type:", "", true},
		{"typ", "type:", "typ", false},
		{"ТИП:баг", "тип:", "баг", true},
		// The Kelvin sign (3 bytes) folds to "k" (1 byte): cutting by the
		// prefix's length in bytes would split a rune.
		{"\u212aind:x", "kind:", "x", true},
	} {
		rest, ok := CutPrefixFold(tc.s, tc.prefix)
		if rest != tc.rest || ok != tc.ok {
			t.Errorf("CutPrefixFold(%q, %q) = %q, %v; want %q, %v", tc.s, tc.prefix, rest, ok, tc.rest, tc.ok)
		}
	}
}

func TestSingleLine(t *testing.T) {
	in := "a\u202e b\r\n\tc\x07d"
	if got, want := SingleLine(in), "a b c d"; got != want {
		t.Errorf("SingleLine(%q) = %q, want %q", in, got, want)
	}
}
