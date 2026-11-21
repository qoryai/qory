package sourcetext_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/qoryai/qory/internal/sourcetext"
)

// TestHiddenIsTheOldTable is Hidden for every code point against the table that Hidden
// had before it was built on primitives.StopsLine.
func TestHiddenIsTheOldTable(t *testing.T) {
	old := func(r rune) bool {
		switch r {
		case '\n', '\t', 0x200C, 0x200D:
			return false
		case 0x2028, 0x2029:
			return true
		}
		return unicode.IsControl(r) || unicode.Is(unicode.Cf, r)
	}
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if got := sourcetext.Hidden(r); got != old(r) {
			t.Fatalf("U+%04X: Hidden is %v, the old table has %v", r, got, old(r))
		}
	}
}

func TestHidden(t *testing.T) {
	tests := []struct {
		name string
		r    rune
		want bool
	}{
		{"tab", 0x09, false},
		{"newline", 0x0A, false},
		{"zero width non-joiner", 0x200C, false},
		{"zero width joiner", 0x200D, false},
		{"letter", 'a', false},
		{"space", ' ', false},
		{"accented letter", 'é', false},
		{"emoji", 0x1F600, false},
		{"replacement character", 0xFFFD, false},
		{"NUL", 0x00, true},
		{"carriage return", 0x0D, true},
		{"DEL", 0x7F, true},
		{"next line", 0x85, true},
		{"soft hyphen", 0xAD, true},
		{"Arabic number sign", 0x0600, true},
		{"Arabic letter mark", 0x061C, true},
		{"zero width space", 0x200B, true},
		{"right-to-left override", 0x202E, true},
		{"line separator", 0x2028, true},
		{"paragraph separator", 0x2029, true},
		{"word joiner", 0x2060, true},
		{"byte order mark", 0xFEFF, true},
		{"tag begin", 0xE0001, true},
		{"tag letter A", 0xE0041, true},
		{"tag first space", 0xE0020, true},
		{"tag cancel", 0xE007F, true},
		{"negative rune", -1, false},
		{"above Unicode", 0x110000, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourcetext.Hidden(tt.r); got != tt.want {
				t.Errorf("Hidden(U+%04X) = %v, want %v", tt.r, got, tt.want)
			}
		})
	}
}

func TestEscape(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"plain", "hello, world", "hello, world"},
		{"tab and newline", "a\tb\nc", "a\tb\nc"},
		{"joiners", "a\u200Cb\u200Dc", "a\u200Cb\u200Dc"},
		{"emoji", "ok 😀", "ok 😀"},
		{"NUL", "a\x00b", "a<U+0000>b"},
		{"carriage return", "a\r\nb", "a<U+000D>\nb"},
		{"DEL", "a\x7Fb", "a<U+007F>b"},
		{"next line", "a\u0085b", "a<U+0085>b"},
		{"soft hyphen", "a\u00ADb", "a<U+00AD>b"},
		{"Arabic number sign", "a\u0600b", "a<U+0600>b"},
		{"Arabic letter mark", "a\u061Cb", "a<U+061C>b"},
		{"zero width space", "a\u200Bb", "a<U+200B>b"},
		{"right-to-left override", "a\u202Eb", "a<U+202E>b"},
		{"line separator", "a\u2028b", "a<U+2028>b"},
		{"paragraph separator", "a\u2029b", "a<U+2029>b"},
		{"word joiner", "a\u2060b", "a<U+2060>b"},
		{"byte order mark", "\uFEFFa", "<U+FEFF>a"},
		{"tag begin", "a\U000E0001b", "a<U+E0001>b"},
		{"tag letter A", "a\U000E0041b", "a<U+E0041>b"},
		{"tag word", "x\U000E0068\U000E0069", "x<U+E0068><U+E0069>"},
		{"literal escape kept", "<U+0041>", "<U+0041>"},
		{"literal and real", "<U+0041>\u202E", "<U+0041><U+202E>"},
		{"invalid byte kept", "a\xffb", "a\xffb"},
		{"invalid byte before hidden", "a\xff\u202Eb\xc3", "a\xff<U+202E>b\xc3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sourcetext.Escape(tt.in); got != tt.want {
				t.Errorf("Escape(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEscapeJSON(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"escaped override", `{"a":"x\u202ey"}`, `{"a":"x<U+202E>y"}`},
		{"surrogate pair tag letter", `{"a":"\udb40\udc41"}`, `{"a":"<U+E0041>"}`},
		{"raw override", "{\"a\":\"x\u202Ey\"}", `{"a":"x<U+202E>y"}`},
		{"member name", `{"k\u202e":1}`, `{"k<U+202E>":1}`},
		{"name and value", `{"k\u0000":"\u0000"}`, `{"k<U+0000>":"<U+0000>"}`},
		{"escaped control", `["a\rb"]`, `["a<U+000D>b"]`},
		{"top-level string", `"\u202e"`, `"<U+202E>"`},
		{
			"order and numbers kept",
			`{"z":1.50,"a":["\u202e",1e3,-0.0,true,null],"m":{"y":2E+2}}`,
			`{"z":1.50,"a":["<U+202E>",1e3,-0.0,true,null],"m":{"y":2E+2}}`,
		},
		{"angle brackets and ampersand", `{"a":"<b>&\u202e</b>"}`, `{"a":"<b>&<U+202E></b>"}`},
		{
			"formatting kept",
			"{\n  \"a\" : [ \"\\u202e\" ,\t\"ok\\n\" ]\n}\n",
			"{\n  \"a\" : [ \"<U+202E>\" ,\t\"ok\\n\" ]\n}\n",
		},
		{
			"CRLF between tokens kept",
			"{\r\n  \"a\": \"x\u202Ey\",\r\n  \"b\": \"c\"\r\n}\r\n",
			"{\r\n  \"a\": \"x<U+202E>y\",\r\n  \"b\": \"c\"\r\n}\r\n",
		},
		{
			"unchanged string keeps its escapes",
			`["\u0041\/","\u202e"]`,
			`["\u0041\/","<U+202E>"]`,
		},
		{"duplicate names", `{"a":1,"a\u202e":2}`, `{"a":1,"a<U+202E>":2}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sourcetext.EscapeJSON([]byte(tt.in))
			if err != nil {
				t.Fatalf("EscapeJSON(%q) error: %v", tt.in, err)
			}
			if string(got) != tt.want {
				t.Errorf("EscapeJSON(%q) = %q, want %q", tt.in, got, tt.want)
			}
			if !json.Valid(got) {
				t.Errorf("EscapeJSON(%q) = %q, which is not valid JSON", tt.in, got)
			}
		})
	}
}

func TestEscapeJSONNothingHidden(t *testing.T) {
	for _, in := range []string{
		`{"a":"b","n":1.50}`,
		"{\r\n  \"a\": \"tab\\tnewline\\n\",\r\n  \"n\": 1e3\r\n}\r\n",
		`["\u200c\u200d","<U+0041>","<b>&"]`,
		`[]`, `{}`, `null`, `1.50`, ` "x" `,
		`{"a":"\ud83d\ude00"}`,
	} {
		got, err := sourcetext.EscapeJSON([]byte(in))
		if err != nil {
			t.Fatalf("EscapeJSON(%q) error: %v", in, err)
		}
		if !bytes.Equal(got, []byte(in)) {
			t.Errorf("EscapeJSON(%q) = %q, want the same bytes", in, got)
		}
	}
}

func TestEscapeJSONInvalid(t *testing.T) {
	for _, in := range []string{
		``, `   `, `{`, `{"a":}`, `{"a":1,}`, `[1 2]`, `{"a":"x"} {"b":1}`, `1 2`,
		`{"a":"x"} x`, `{a:1}`, `"\ud800"`, "\"\xff\"", `"abc`, `{"a":01}`,
		"\uFEFF{}", `{"a":"x\u202e"} ,`,
	} {
		if got, err := sourcetext.EscapeJSON([]byte(in)); err == nil {
			t.Errorf("EscapeJSON(%q) = %q, want an error", in, got)
		}
	}
}

func TestEscapeJSONFile(t *testing.T) {
	const bom = "\xef\xbb\xbf"
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			"BOM and CRLF kept",
			bom + "{\r\n  \"a\": \"x\u202Ey\",\r\n  \"b\": \"c\"\r\n}\r\n",
			bom + "{\r\n  \"a\": \"x<U+202E>y\",\r\n  \"b\": \"c\"\r\n}\r\n",
		},
		{"BOM and JSON escape", bom + `{"a":"x\u202ey"}`, bom + `{"a":"x<U+202E>y"}`},
		{
			"lone surrogate kept, other string escaped",
			`["\ud800","\u202e","\udc00x"]`,
			`["\ud800","<U+202E>","\udc00x"]`,
		},
		{"invalid UTF-8 kept", "[\"caf\xe9\",\"\\u202e\"]", "[\"caf\xe9\",\"<U+202E>\"]"},
		{"hidden and invalid in one string", "[\"\xff\\u202e\"]", "[\"\uFFFD<U+202E>\"]"},
		{"member name", `{"k\u202e":"\ud800"}`, `{"k<U+202E>":"\ud800"}`},
		{"valid input as EscapeJSON", `{"a":"\udb40\udc41"}`, `{"a":"<U+E0041>"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := sourcetext.EscapeJSONFile([]byte(tt.in))
			if err != nil {
				t.Fatalf("EscapeJSONFile(%q) error: %v", tt.in, err)
			}
			if string(got) != tt.want {
				t.Errorf("EscapeJSONFile(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestEscapeJSONFileNothingHidden(t *testing.T) {
	for _, in := range []string{
		"\xef\xbb\xbf{\"a\":\"b\"}", `["\ud800"]`, "[\"\xff\"]", "\xef\xbb\xbf1", `{"a":1}`,
	} {
		got, err := sourcetext.EscapeJSONFile([]byte(in))
		if err != nil || string(got) != in {
			t.Errorf("EscapeJSONFile(%q) = %q, %v, want the same bytes", in, got, err)
		}
	}
}

func TestEscapeJSONFileInvalid(t *testing.T) {
	for _, in := range []string{
		``, "\xef\xbb\xbf", `{`, `{ x\u202e`, `{"a":1} {"b":1}`, "\xef\xbb\xbf{",
		"\xef\xbb\xbf\xef\xbb\xbf{}", `{"a":"x"} ,`,
	} {
		if got, err := sourcetext.EscapeJSONFile([]byte(in)); err == nil {
			t.Errorf("EscapeJSONFile(%q) = %q, want an error", in, got)
		}
	}
}

func FuzzEscapeJSONFile(f *testing.F) {
	for _, seed := range []string{
		`{"a":"x\u202ey"}`, `["\ud800","\u202e"]`, "{\r\n\"k\\u0000\": null\r\n}",
		"[\"\xff\u202e\"]", `1`,
	} {
		f.Add([]byte(seed), true)
		f.Add([]byte(seed), false)
	}
	f.Fuzz(func(t *testing.T, doc []byte, withBOM bool) {
		const bom = "\xef\xbb\xbf"
		if withBOM {
			doc = append([]byte(bom), doc...)
		}
		out, err := sourcetext.EscapeJSONFile(doc)
		if err != nil {
			return
		}
		body := bytes.TrimPrefix(out, []byte(bom))
		if withBOM && len(body) == len(out) {
			t.Fatalf("EscapeJSONFile(%q) = %q, which lost the byte order mark", doc, out)
		}
		if !json.Valid(body) {
			t.Fatalf("EscapeJSONFile(%q) = %q, which is not valid JSON after its byte order mark", doc, out)
		}
		again, err := sourcetext.EscapeJSONFile(out)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("EscapeJSONFile is not stable: %q then %q (%v)", out, again, err)
		}
	})
}

func FuzzEscapeJSON(f *testing.F) {
	for _, seed := range []string{
		`{"a":"x\u202ey"}`, `["\udb40\udc41",1.50,1e3]`, "{\r\n\"k\\u0000\": null\r\n}",
		`"plain"`, `{"a":{"b":["\r","\u2028"]}}`, `1`,
	} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, doc []byte) {
		out, err := sourcetext.EscapeJSON(doc)
		if err != nil {
			return
		}
		if !json.Valid(out) {
			t.Fatalf("EscapeJSON(%q) = %q, which is not valid JSON", doc, out)
		}
		var v any
		dec := json.NewDecoder(bytes.NewReader(out))
		dec.UseNumber() // a number such as 1e999 does not fit a float64
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("Decode(%q): %v", out, err)
		}
		checkNoHidden(t, v, out)
		again, err := sourcetext.EscapeJSON(out)
		if err != nil || !bytes.Equal(again, out) {
			t.Fatalf("EscapeJSON is not stable: %q then %q (%v)", out, again, err)
		}
	})
}

// checkNoHidden fails when a string value or member name in v has a hidden rune.
func checkNoHidden(t *testing.T, v any, out []byte) {
	t.Helper()
	switch v := v.(type) {
	case string:
		for _, r := range v {
			if sourcetext.Hidden(r) {
				t.Fatalf("output %q has hidden rune U+%04X in string %q", out, r, v)
			}
		}
		if !utf8.ValidString(v) {
			t.Fatalf("output %q has invalid UTF-8 in string %q", out, v)
		}
	case []any:
		for _, e := range v {
			checkNoHidden(t, e, out)
		}
	case map[string]any:
		for k, e := range v {
			checkNoHidden(t, k, out)
			checkNoHidden(t, e, out)
		}
	}
}
