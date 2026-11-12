package instance

import (
	"strings"
	"testing"
)

func adviceCodes(list []Advice) map[string]bool {
	m := map[string]bool{}
	for _, a := range list {
		m[a.Code] = true
	}
	return m
}

func memInfo(frag, used, maxmem, policy string) map[string]string {
	m := map[string]string{"mem_fragmentation_ratio": frag, "used_memory": used, "maxmemory": maxmem}
	if policy != "" {
		m["maxmemory_policy"] = policy
	}
	return m
}

func TestMemoryAdviceRules(t *testing.T) {
	cases := []struct {
		name string
		info map[string]string
		want map[string]bool
	}{
		{"frag high and used above floor", memInfo("1.6", "100000000", "0", ""), map[string]bool{"frag_high": true, "maxmemory_unset": true}},
		{"frag high but used below floor", memInfo("1.6", "10000000", "0", ""), map[string]bool{"maxmemory_unset": true}},
		{"frag exactly 1.5 is not high", memInfo("1.5", "100000000", "1000000000", ""), map[string]bool{}},
		{"frag below 1.0 suggests swap", memInfo("0.9", "100000000", "1000000000", ""), map[string]bool{"frag_low": true}},
		{"frag exactly 1.0 is fine", memInfo("1.0", "100000000", "1000000000", ""), map[string]bool{}},
		{"frag zero means field absent, no swap advice", memInfo("0", "100000000", "1000000000", ""), map[string]bool{}},
		{"maxmemory 0 means unlimited", memInfo("1.0", "100", "0", ""), map[string]bool{"maxmemory_unset": true}},
		{"used 91 percent of maxmemory", memInfo("1.0", "91", "100", "allkeys-lru"), map[string]bool{"maxmemory_near": true}},
		{"used exactly 90 percent is not near", memInfo("1.0", "90", "100", "allkeys-lru"), map[string]bool{}},
		{"empty input yields nothing", map[string]string{}, map[string]bool{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := adviceCodes(MemoryAdvice(tc.info))
			for code := range tc.want {
				if !got[code] {
					t.Errorf("missing advice %q, got %v", code, got)
				}
			}
			for code := range got {
				if !tc.want[code] {
					t.Errorf("unexpected advice %q", code)
				}
			}
		})
	}
}

func TestMemoryAdviceNearMentionsPolicyAndNumbers(t *testing.T) {
	list := MemoryAdvice(memInfo("1.0", "95", "100", "allkeys-lru"))
	var msg string
	for _, a := range list {
		if a.Code == "maxmemory_near" {
			msg = a.Message
		}
	}
	if msg == "" {
		t.Fatalf("no maxmemory_near advice: %+v", list)
	}
	if !strings.Contains(msg, "allkeys-lru") || !strings.Contains(msg, "100") {
		t.Fatalf("message should cite policy and maxmemory value: %q", msg)
	}
}

func TestMemoryAdviceNonNilAndMissingFields(t *testing.T) {
	if MemoryAdvice(nil) == nil {
		t.Fatal("nil input must return non-nil slice")
	}
	if got := MemoryAdvice(map[string]string{"used_memory": "abc"}); len(got) != 0 {
		t.Fatalf("garbage input should yield no advice, got %+v", got)
	}
}
