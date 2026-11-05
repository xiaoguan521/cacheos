package textdiff

import (
	"strings"
	"testing"
)

func TestUnified(t *testing.T) {
	if Unified("x", "a\n", "a\n") != "" {
		t.Fatal("equal inputs should give empty diff")
	}
	a := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n"
	b := "1\n2\n3\n4\nfive\n6\n7\n8\n9\n10\n11\n"
	d := Unified("f.conf", a, b)
	for _, want := range []string{"--- a/f.conf", "+++ b/f.conf", "-5", "+five", "+11", "@@ -2,9 +2,10 @@"} {
		if !strings.Contains(d, want) {
			t.Fatalf("missing %q in:\n%s", want, d)
		}
	}
	if d2 := Unified("new", "", "x\n"); !strings.Contains(d2, "+x") {
		t.Fatalf("new file diff: %s", d2)
	}
}
