package paramcat

import (
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	ok := map[[2]string]string{
		{"maxmemory", "1gb"}:                                 "1073741824",
		{"maxmemory", "512mb"}:                               "536870912",
		{"maxmemory-policy", "ALLKEYS-LRU"}:                  "allkeys-lru",
		{"appendonly", "true"}:                               "yes",
		{"save", "3600 1 300 100"}:                           "3600 1 300 100",
		{"save", ""}:                                         "",
		{"slowlog-log-slower-than", "5000"}:                  "5000", // 别名
		{"notify-keyspace-events", "Ex"}:                     "Ex",
		{"client-output-buffer-limit", "pubsub 32mb 8mb 60"}: "pubsub 32mb 8mb 60",
		{"maxmemory-clients", "10%"}:                         "10%",
	}
	for in, want := range ok {
		got, err := Normalize(in[0], in[1])
		if err != nil || got != want {
			t.Errorf("Normalize(%q, %q) = %q, %v; want %q", in[0], in[1], got, err, want)
		}
	}
	bad := [][2]string{
		{"port", "7000"},                                // 编排管理的参数不开放
		{"dir", "/etc"},                                 // 同上
		{"requirepass", "x"},                            // 口令不经参数目录
		{"maxmemory-policy", "random"},                  // 枚举外的值
		{"maxclients", "0"},                             // 低于下限
		{"save", "3600"},                                // 必须成对
		{"appendonly", "maybe"},                         // 非布尔
		{"maxmemory", "1gb\nrename-command FLUSHALL x"}, // 注入换行
		{"notify-keyspace-events", "Ex;"},
		{"client-output-buffer-limit", "normal 0 0"},
		{"databases", "0"},
	}
	for _, in := range bad {
		if v, err := Normalize(in[0], in[1]); err == nil {
			t.Errorf("Normalize(%q, %q) accepted as %q", in[0], in[1], v)
		}
	}
}

func TestDiffSplitsRestart(t *testing.T) {
	old := map[string]string{"maxmemory-policy": "noeviction", "databases": "16", "hz": "20"}
	nw := map[string]string{"maxmemory-policy": "allkeys-lru", "databases": "32"}
	changed, hot, restart := Diff(old, nw)
	if changed["hz"] != "10" { // 删除即恢复默认
		t.Fatalf("hz reset: %v", changed)
	}
	if strings.Join(hot, ",") != "hz,maxmemory-policy" || strings.Join(restart, ",") != "databases" {
		t.Fatalf("hot=%v restart=%v", hot, restart)
	}
}

func TestBuiltinTemplatesValid(t *testing.T) {
	for _, tpl := range Builtin {
		if _, err := NormalizeAll(tpl.Params); err != nil {
			t.Errorf("template %s: %v", tpl.Name, err)
		}
	}
}

func TestCatalogCoversDefaults(t *testing.T) {
	for _, p := range All() {
		if p.Default == "" && p.Type != "special" && p.Type != "string" {
			t.Errorf("%s has no default", p.Name)
		}
		if p.Default != "" {
			if _, err := Normalize(p.Name, p.Default); err != nil {
				t.Errorf("default of %s does not validate: %v", p.Name, err)
			}
		}
	}
}
