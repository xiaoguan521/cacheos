package api

import (
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/gateway"
	"github.com/xiaoguan521/cacheos/internal/console/licsvc"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/console/store/storetest"
	"github.com/xiaoguan521/cacheos/internal/license"
	"github.com/xiaoguan521/cacheos/internal/pkg/audit"
)

// parsedSample 是测试侧解析出的一条样本。
type parsedSample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

// parseProm 按 Prometheus 文本格式解析（测试独立实现，不复用渲染代码）。
func parseProm(t *testing.T, body string) (samples []parsedSample, types map[string]string) {
	t.Helper()
	types = map[string]string{}
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if line == "" {
			t.Fatalf("empty line in exposition")
		}
		if strings.HasPrefix(line, "# HELP ") {
			continue
		}
		if strings.HasPrefix(line, "# TYPE ") {
			f := strings.Fields(line)
			types[f[2]] = f[3]
			continue
		}
		open := strings.IndexByte(line, '{')
		closeIdx := strings.LastIndexByte(line, '}')
		if open < 0 || closeIdx < open {
			t.Fatalf("bad sample line: %q", line)
		}
		s := parsedSample{Name: line[:open], Labels: map[string]string{}}
		rest := line[open+1 : closeIdx]
		for rest != "" {
			eq := strings.IndexByte(rest, '=')
			if eq < 0 || eq+1 >= len(rest) || rest[eq+1] != '"' {
				t.Fatalf("bad label in %q", line)
			}
			name := rest[:eq]
			var val strings.Builder
			i := eq + 2
			for ; ; i++ {
				if i >= len(rest) {
					t.Fatalf("unterminated label value in %q", line)
				}
				c := rest[i]
				if c == '"' {
					break
				}
				if c == '\\' {
					i++
					switch rest[i] {
					case '\\':
						val.WriteByte('\\')
					case '"':
						val.WriteByte('"')
					case 'n':
						val.WriteByte('\n')
					default:
						t.Fatalf("invalid escape in %q", line)
					}
					continue
				}
				val.WriteByte(c)
			}
			s.Labels[name] = val.String()
			rest = rest[i+1:]
			rest = strings.TrimPrefix(rest, ",")
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(line[closeIdx+1:]), 64)
		if err != nil {
			t.Fatalf("bad value in %q: %v", line, err)
		}
		s.Value = v
		samples = append(samples, s)
	}
	return samples, types
}

func findSample(t *testing.T, samples []parsedSample, name string, labels map[string]string) float64 {
	t.Helper()
	for _, s := range samples {
		if s.Name != name || len(s.Labels) != len(labels) {
			continue
		}
		ok := true
		for k, v := range labels {
			if s.Labels[k] != v {
				ok = false
				break
			}
		}
		if ok {
			return s.Value
		}
	}
	t.Fatalf("sample %s %v not found", name, labels)
	return 0
}

func statusJSON(t *testing.T, st *agentv1.InstanceStatus) string {
	t.Helper()
	b, err := protojson.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func testFixture(t *testing.T) ([]store.Host, map[uint]string, []store.Instance, func(uint) bool) {
	t.Helper()
	seen := time.Now()
	hosts := []store.Host{
		{ID: 1, Name: "h1", CPUPercent: 12.5, MemUsedBytes: 4000, MemTotalBytes: 8000, DiskFreeBytes: 123456, Status: "online", LastSeenAt: &seen},
		{ID: 2, Name: "h2-offline", Status: "offline"},
	}
	units := map[uint]string{1: `u"1\x`}
	master := &agentv1.InstanceStatus{
		Port: 7001, Role: "master", State: agentv1.State_STATE_RUNNING,
		UsedMemory: 12345, Maxmemory: 1 << 30, ConnectedClients: 7, OpsPerSec: 99,
		KeyspaceHits: 300, KeyspaceMisses: 100, Keys: 42, MemFragmentationRatio: 1.25,
		MasterReplOffset: 5000, RdbLastBgsaveStatus: "ok", AofEnabled: true, AofLastWriteStatus: "err",
		Replicas: []*agentv1.ReplicaInfo{{Ip: "10.0.0.2", Port: 7002, Lag: 30}, {Ip: "10.0.0.3", Port: 7003, Lag: 80}},
	}
	replica := &agentv1.InstanceStatus{Port: 7002, Role: "slave", State: agentv1.State_STATE_RUNNING, MasterLinkStatus: "up", MasterReplOffset: 4950}
	insts := []store.Instance{
		{ID: 1, UnitID: 1, HostID: 1, Port: 7001, Kind: "server", Role: "master", State: "running", Status: statusJSON(t, master)},
		{ID: 2, UnitID: 1, HostID: 1, Port: 7002, Kind: "server", Role: "replica", State: "running", Status: statusJSON(t, replica)},
		{ID: 3, UnitID: 1, HostID: 2, Port: 7003, Kind: "server", Role: "master", State: "running", Status: statusJSON(t, master)},
		{ID: 4, UnitID: 1, HostID: 1, Port: 26379, Kind: "sentinel", Role: "sentinel", State: "stopped"},
	}
	online := func(id uint) bool { return id == 1 }
	return hosts, units, insts, online
}

func TestPromTextValuesAndTypes(t *testing.T) {
	hosts, units, insts, online := testFixture(t)
	body := string(renderProm(buildPromFamilies(hosts, online, units, insts)))
	samples, types := parseProm(t, body)

	if types["cacheos_instance_keyspace_hits_total"] != "counter" || types["cacheos_instance_used_memory_bytes"] != "gauge" {
		t.Fatalf("wrong TYPE lines: %v", types)
	}
	for name := range types {
		if !strings.HasPrefix(name, "cacheos_") {
			t.Fatalf("bad metric name %q", name)
		}
	}

	unit := `u"1\x`
	ms := map[string]string{"unit": unit, "host": "h1", "port": "7001", "role": "master"}
	if v := findSample(t, samples, "cacheos_host_up", map[string]string{"host": "h1"}); v != 1 {
		t.Errorf("host_up h1 = %v", v)
	}
	if v := findSample(t, samples, "cacheos_host_up", map[string]string{"host": "h2-offline"}); v != 0 {
		t.Errorf("host_up offline = %v", v)
	}
	if v := findSample(t, samples, "cacheos_host_cpu_percent", map[string]string{"host": "h1"}); v != 12.5 {
		t.Errorf("cpu = %v", v)
	}
	for _, s := range samples {
		if s.Name == "cacheos_host_cpu_percent" && s.Labels["host"] == "h2-offline" {
			t.Errorf("never-reported host must not expose resource gauges")
		}
	}
	if v := findSample(t, samples, "cacheos_host_data_disk_free_bytes", map[string]string{"host": "h1"}); v != 123456 {
		t.Errorf("disk free = %v", v)
	}
	if v := findSample(t, samples, "cacheos_instance_up", ms); v != 1 {
		t.Errorf("instance_up = %v", v)
	}
	// 离线主机上的实例即使状态记录为 running 也不算存活
	if v := findSample(t, samples, "cacheos_instance_up", map[string]string{"unit": unit, "host": "h2-offline", "port": "7003", "role": "master"}); v != 0 {
		t.Errorf("instance on offline host up = %v", v)
	}
	checks := map[string]float64{
		"cacheos_instance_used_memory_bytes":         12345,
		"cacheos_instance_maxmemory_bytes":           1 << 30,
		"cacheos_instance_connected_clients":         7,
		"cacheos_instance_ops_per_sec":               99,
		"cacheos_instance_keyspace_hits_total":       300,
		"cacheos_instance_keyspace_misses_total":     100,
		"cacheos_instance_keys":                      42,
		"cacheos_instance_mem_fragmentation_ratio":   1.25,
		"cacheos_instance_repl_offset":               5000,
		"cacheos_instance_replication_lag_max_bytes": 80,
		"cacheos_instance_last_bgsave_ok":            1,
		"cacheos_instance_last_aof_write_ok":         0,
	}
	for name, want := range checks {
		if got := findSample(t, samples, name, ms); got != want {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	rep := map[string]string{"unit": unit, "host": "h1", "port": "7002", "role": "replica"}
	if v := findSample(t, samples, "cacheos_instance_replica_link_up", rep); v != 1 {
		t.Errorf("replica link_up = %v", v)
	}
	for _, s := range samples {
		if s.Labels["port"] == "26379" && s.Name != "cacheos_instance_up" {
			t.Errorf("sentinel should only report up, got %s", s.Name)
		}
		if _, bad := s.Labels["password"]; bad {
			t.Errorf("forbidden label in %s", s.Name)
		}
	}
	if strings.Contains(body, "requirepass") || strings.Contains(body, "masterauth") {
		t.Fatal("credential-like names leaked")
	}
}

func TestPromLabelEscaping(t *testing.T) {
	hosts := []store.Host{{ID: 1, Name: "a\\b\"c\nd", Status: "online"}}
	insts := []store.Instance{{ID: 1, UnitID: 9, HostID: 1, Port: 7001, Kind: "server", Role: "master", State: "running"}}
	body := string(renderProm(buildPromFamilies(hosts, func(uint) bool { return true }, map[uint]string{9: "x\"y"}, insts)))
	if !strings.Contains(body, `host="a\\b\"c\nd"`) {
		t.Fatalf("host label not escaped:\n%s", body)
	}
	if !strings.Contains(body, `unit="x\"y"`) {
		t.Fatalf("unit label not escaped:\n%s", body)
	}
	if strings.Contains(body, "c\nd") {
		t.Fatal("raw newline leaked into a label value")
	}
	samples, _ := parseProm(t, body)
	findSample(t, samples, "cacheos_host_up", map[string]string{"host": "a\\b\"c\nd"})
}

func TestPromHelpEscaping(t *testing.T) {
	if got := promEscapeHelp("a\\b\nc"); got != `a\\b\nc` {
		t.Fatalf("help escape = %q", got)
	}
}

func TestPromEndpointRequiresLoginAndHidesCredentials(t *testing.T) {
	st := storetest.Open(t)
	lic := &licsvc.Service{Store: st, DataDir: t.TempDir(), Keys: license.BuiltinKeyRing()}
	if err := lic.Init(); err != nil {
		t.Fatal(err)
	}
	s := &Server{Store: st, Lic: lic, GW: &gateway.Server{}, Audit: &audit.Logger{DB: st.DB}}
	h, err := s.Handler()
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(h)
	defer ts.Close()

	res, err := http.Get(ts.URL + "/api/v1/metrics/prometheus")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous access: got %d, want 401", res.StatusCode)
	}

	// 写入带凭据字段的数据：指标输出中不得出现它们
	const fp = "FPSECRET-7781"
	const masterAuth = "MASTERAUTH-9932"
	const pw = "PWSECRET-5520"
	if err := st.DB.Create(&store.Host{Name: "h1", Status: "online", Fingerprint: fp, CertSerial: fp}).Error; err != nil {
		t.Fatal(err)
	}
	if err := st.DB.Create(&store.Unit{Name: "u1", Mode: "standalone", MasterAuth: masterAuth}).Error; err != nil {
		t.Fatal(err)
	}
	leaky := &agentv1.InstanceStatus{Port: 7001, Role: "master", State: agentv1.State_STATE_RUNNING, UsedMemory: 77,
		Config: map[string]string{"requirepass": pw, "masterauth": masterAuth}}
	if err := st.DB.Create(&store.Instance{UnitID: 1, HostID: 1, Port: 7001, Kind: "server", Role: "master", State: "running", Status: statusJSON(t, leaky)}).Error; err != nil {
		t.Fatal(err)
	}

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Jar: jar}
	res, err = client.Do(jsonRequest(t, http.MethodPost, ts.URL+"/api/v1/setup", map[string]string{"username": "admin", "password": "C0nsole!2026Safe"}))
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("setup: %d", res.StatusCode)
	}

	res, err = client.Get(ts.URL + "/api/v1/metrics/prometheus")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("login access: %d %s", res.StatusCode, body)
	}
	if ct := res.Header.Get("Content-Type"); ct != "text/plain; version=0.0.4; charset=utf-8" {
		t.Fatalf("content type = %q", ct)
	}
	samples, _ := parseProm(t, string(body))
	if v := findSample(t, samples, "cacheos_instance_used_memory_bytes", map[string]string{"unit": "u1", "host": "h1", "port": "7001", "role": "master"}); v != 77 {
		t.Fatalf("used memory = %v", v)
	}
	for _, secret := range []string{fp, masterAuth, pw, "requirepass", "masterauth"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("response leaks %q", secret)
		}
	}
}
