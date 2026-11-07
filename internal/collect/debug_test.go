package collect

import (
	"bytes"
	"testing"
)

func TestElideLists(t *testing.T) {
	cases := map[string]string{
		`Computer in ("a","b")`:              `Computer in (<2 values>)`,
		`Computer in~ ("a", "b", "c") and x`: `Computer in~ (<3 values>) and x`,
		`Computer !in ("a,1","b")`:           `Computer !in (<2 values>)`,
		`a in ("x") | where b in ("y","z")`:  `a in (<1 values>) | where b in (<2 values>)`,
		`where Name == "in (a, b)"`:          `where Name == "in (a, b)"`,
		`T | take 5`:                         `T | take 5`,
	}
	for in, want := range cases {
		if got := ElideLists(in); got != want {
			t.Errorf("ElideLists(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMaskID(t *testing.T) {
	if got := MaskID("00000000-0000-0000-0000-00000000000a"); got != "…000a" {
		t.Errorf("%q", got)
	}
	if MaskID("") != "" {
		t.Error("empty")
	}
}

func TestDebugf(t *testing.T) {
	var b bytes.Buffer
	SetDebug(&b)
	defer SetDebug(nil)
	Debugf("hello %d", 1)
	Debugf("x\n")
	if b.String() != "debug: hello 1\ndebug: x\n" {
		t.Errorf("%q", b.String())
	}
	SetDebug(nil)
	Debugf("off")
	if b.Len() != len("debug: hello 1\ndebug: x\n") {
		t.Error("wrote while off")
	}
}
