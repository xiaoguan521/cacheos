package orch

import (
	"errors"
	"testing"

	"github.com/xiaoguan521/cacheos/internal/console/store"
)

func TestHostMissingModule(t *testing.T) {
	h := store.Host{Modules: "json,bloom"}
	if m := hostMissingModule(h, nil); m != "" {
		t.Errorf("no modules needed, got missing %q", m)
	}
	if m := hostMissingModule(h, []string{"json", "bloom"}); m != "" {
		t.Errorf("all present, got missing %q", m)
	}
	if m := hostMissingModule(h, []string{"json", "search"}); m != "search" {
		t.Errorf("missing = %q, want search", m)
	}
	if m := hostMissingModule(store.Host{}, []string{"json"}); m != "json" {
		t.Errorf("host without modules: missing = %q", m)
	}
}

// 创建 / 扩容选主机时，缺少所选模块的主机不是候选；没有任何主机满足时返回 ErrNoHosts，并说明缺哪个模块。
func TestPickScaleHostSkipsHostsWithoutModules(t *testing.T) {
	s := &Service{}
	loads := map[uint]*hostLoad{
		1: {h: store.Host{ID: 1, Name: "a", Zone: "az-a", Modules: ""}, ports: map[uint32]bool{}},
		2: {h: store.Host{ID: 2, Name: "b", Zone: "az-b", Modules: "json"}, ports: map[uint32]bool{}},
	}
	got, err := s.pickScaleHostFor(loads, nil, map[string]bool{}, 64, 0, []string{"json"})
	if err != nil || got.h.ID != 2 {
		t.Fatalf("picked %v, %v; want host 2", got, err)
	}
	_, err = s.pickScaleHostFor(loads, nil, map[string]bool{}, 64, 0, []string{"search"})
	if !errors.Is(err, ErrNoHosts) {
		t.Errorf("err = %v, want ErrNoHosts", err)
	}
}
