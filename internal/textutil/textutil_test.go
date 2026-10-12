package textutil

import "testing"

func TestBaybayinWidth(t *testing.T) {
	// HA, I-sign (combining), MA, YA, virama (combining): 3 base glyphs.
	if w := Width("ᜑᜒᜋᜌ᜔"); w < 3 || w > 5 {
		t.Fatalf("width = %d", w)
	}
	if got := Pad("ᜑᜒᜋᜌ᜔", 8); Width(got) != 8 {
		t.Fatalf("pad width = %d", Width(got))
	}
}

func TestTruncateAndPad(t *testing.T) {
	cases := []struct {
		in   string
		w    int
		want int
	}{{"hello world", 5, 5}, {"日本語テキスト", 6, 6}, {"short", 10, 10}, {"", 3, 3}}
	for _, c := range cases {
		if got := Width(Pad(c.in, c.w)); got != c.want {
			t.Errorf("Pad(%q,%d) width %d", c.in, c.w, got)
		}
	}
	if Truncate("abc", 0) != "" {
		t.Error("zero width")
	}
}

func TestSanitize(t *testing.T) {
	if got := Sanitize("a\x1b[31mred\x1b[0m\x07b"); got != "aredb" {
		t.Fatalf("sanitize = %q", got)
	}
}

func TestWrap(t *testing.T) {
	lines := Wrap("one two three four five", 9)
	for _, l := range lines {
		if Width(l) > 9 {
			t.Errorf("line too wide: %q", l)
		}
	}
	if len(Wrap("a\n\nb", 10)) != 3 {
		t.Error("blank lines not preserved")
	}
}
