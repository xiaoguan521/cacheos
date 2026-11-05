package collect

import "testing"

func TestParseTHP(t *testing.T) {
	for in, want := range map[string]string{"always madvise [never]\n": "never", "[always] madvise never": "always", "madvise": "madvise"} {
		if got := ParseTHP(in); got != want {
			t.Errorf("ParseTHP(%q) = %q", in, got)
		}
	}
}

func TestHostSample(t *testing.T) {
	h := &Host{}
	h.Sample()
	if _, mem := h.Sample(); mem == 0 || MemTotal() == 0 {
		t.Fatal("no memory figures")
	}
	if Tuning().NofileLimit == 0 {
		t.Fatal("no nofile limit")
	}
}
