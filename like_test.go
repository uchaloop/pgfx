package pgfx

import "testing"

func TestEscapeLike(t *testing.T) {
	for _, item := range []struct{ input, want string }{
		{input: ""},
		{input: "Иван", want: "Иван"},
		{input: `50%_off\sale`, want: `50\%\_off\\sale`},
		{input: `a|b.*`, want: `a|b.*`},
		{input: `'quote'`, want: `'quote'`},
	} {
		if got := EscapeLike(item.input); got != item.want {
			t.Fatalf(
				"EscapeLike(%q)=%q, want %q",
				item.input,
				got,
				item.want,
			)
		}
	}
}
