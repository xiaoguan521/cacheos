package orch

import (
	"strings"
	"testing"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/pki"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/license"
)

func licenseWith(features ...license.Feature) func() license.Snapshot {
	return func() license.Snapshot {
		return license.Snapshot{State: license.StateValid, Payload: &license.Payload{Features: features}}
	}
}

// 期望值按产品规则手写：监听地址优先用主机 Address，其次 Hostname；数据面 TLS 只给数据实例；
// 复制 / 集群内部 TLS 取决于许可含 tls_internal；集群形态才有 TlsCluster。
func TestCreateRequestFields(t *testing.T) {
	ca, err := pki.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	withAddr := &store.Host{Name: "h1", Hostname: "h1.local", Address: "10.0.0.1"}
	noAddr := &store.Host{Name: "h2", Hostname: "h2.local"}
	masterInst := &store.Instance{HostID: 1, Port: 7001, Kind: "server", Desired: "master", Shard: 2}

	cases := []struct {
		name    string
		unit    store.Unit
		in      store.Instance
		host    *store.Host
		lic     func() license.Snapshot
		masters map[uint32]*store.Instance
		check   func(t *testing.T, r *agentv1.CreateInstance)
		wantErr string
	}{
		{name: "standalone binds to host address, no TLS", unit: store.Unit{Mode: store.ModeStandalone, RevisionID: "p1"},
			in: store.Instance{Port: 7000, Kind: "server", Desired: "master"}, host: withAddr,
			check: func(t *testing.T, r *agentv1.CreateInstance) {
				if r.BindAddress != "10.0.0.1" || r.Tls != nil || r.ClusterEnabled || r.ReplicaOf != nil || r.RevisionId != "p1" || r.Kind != agentv1.Kind_KIND_SERVER {
					t.Fatalf("unexpected request: %+v", r)
				}
			}},
		{name: "host without address falls back to hostname", unit: store.Unit{Mode: store.ModeStandalone},
			in: store.Instance{Port: 7000, Kind: "server", Desired: "master"}, host: noAddr,
			check: func(t *testing.T, r *agentv1.CreateInstance) {
				if r.BindAddress != "h2.local" {
					t.Fatalf("bind = %q, want h2.local", r.BindAddress)
				}
			}},
		{name: "cluster unit enables cluster mode", unit: store.Unit{Mode: store.ModeCluster},
			in: store.Instance{Port: 7000, Kind: "server", Desired: "master"}, host: withAddr,
			check: func(t *testing.T, r *agentv1.CreateInstance) {
				if !r.ClusterEnabled || r.ReplicaOf != nil {
					t.Fatalf("unexpected request: %+v", r)
				}
			}},
		{name: "TLS without internal-TLS license: client TLS only", unit: store.Unit{Mode: store.ModeReplication, TLS: true},
			in: store.Instance{Port: 7000, Kind: "server", Desired: "master"}, host: withAddr, lic: licenseWith(license.FeatureTLS),
			check: func(t *testing.T, r *agentv1.CreateInstance) {
				if r.Tls == nil || r.Tls.TlsReplication || r.Tls.TlsCluster || len(r.Tls.KeyPem) == 0 || len(r.Tls.CertPem) == 0 || len(r.Tls.CaPem) == 0 {
					t.Fatalf("unexpected TLS material: %+v", r.Tls)
				}
			}},
		{name: "TLS with internal-TLS license on replication: replication TLS, no cluster TLS", unit: store.Unit{Mode: store.ModeReplication, TLS: true},
			in: store.Instance{Port: 7000, Kind: "server", Desired: "master"}, host: withAddr, lic: licenseWith(license.FeatureTLSInternal),
			check: func(t *testing.T, r *agentv1.CreateInstance) {
				if r.Tls == nil || !r.Tls.TlsReplication || r.Tls.TlsCluster {
					t.Fatalf("unexpected TLS flags: %+v", r.Tls)
				}
			}},
		{name: "TLS with internal-TLS license on cluster: both", unit: store.Unit{Mode: store.ModeCluster, TLS: true},
			in: store.Instance{Port: 7000, Kind: "server", Desired: "master"}, host: withAddr, lic: licenseWith(license.FeatureTLSInternal),
			check: func(t *testing.T, r *agentv1.CreateInstance) {
				if r.Tls == nil || !r.Tls.TlsReplication || !r.Tls.TlsCluster {
					t.Fatalf("unexpected TLS flags: %+v", r.Tls)
				}
			}},
		{name: "TLS but no license function: no internal TLS", unit: store.Unit{Mode: store.ModeReplication, TLS: true},
			in: store.Instance{Port: 7000, Kind: "server", Desired: "master"}, host: withAddr,
			check: func(t *testing.T, r *agentv1.CreateInstance) {
				if r.Tls == nil || r.Tls.TlsReplication {
					t.Fatalf("unexpected TLS flags: %+v", r.Tls)
				}
			}},
		{name: "replica points at the master of its own shard", unit: store.Unit{Mode: store.ModeReplication},
			in: store.Instance{Port: 7002, Kind: "server", Desired: "replica", Shard: 2}, host: withAddr,
			masters: map[uint32]*store.Instance{2: masterInst, 0: {HostID: 1, Port: 9999}},
			check: func(t *testing.T, r *agentv1.CreateInstance) {
				if r.ReplicaOf == nil || r.ReplicaOf.Host != "10.0.0.1" || r.ReplicaOf.Port != 7001 {
					t.Fatalf("unexpected replica wiring: %+v", r.ReplicaOf)
				}
			}},
		{name: "replica without a ready master is an error", unit: store.Unit{Mode: store.ModeReplication},
			in: store.Instance{Port: 7002, Kind: "server", Desired: "replica", Shard: 3}, host: withAddr,
			masters: map[uint32]*store.Instance{2: masterInst}, wantErr: "master for shard 3 is not ready"},
		{name: "cluster replicas are wired by the cluster, not ReplicaOf", unit: store.Unit{Mode: store.ModeCluster},
			in: store.Instance{Port: 7002, Kind: "server", Desired: "replica"}, host: withAddr,
			check: func(t *testing.T, r *agentv1.CreateInstance) {
				if r.ReplicaOf != nil {
					t.Fatalf("cluster replica got ReplicaOf: %+v", r.ReplicaOf)
				}
			}},
		{name: "sentinel monitors shard 0 master, gets no TLS and no modules", unit: store.Unit{Mode: store.ModeSentinel, TLS: true, SentinelName: "mymaster", Quorum: 2, Modules: "json"},
			in: store.Instance{Port: 26379, Kind: "sentinel", Desired: "sentinel"}, host: withAddr,
			masters: map[uint32]*store.Instance{0: {HostID: 1, Port: 7000}},
			check: func(t *testing.T, r *agentv1.CreateInstance) {
				if r.Kind != agentv1.Kind_KIND_SENTINEL || r.Tls != nil || len(r.Modules) != 0 ||
					r.SentinelMonitorName != "mymaster" || r.SentinelMasterHost != "10.0.0.1" || r.SentinelMasterPort != 7000 || r.SentinelQuorum != 2 {
					t.Fatalf("unexpected sentinel request: %+v", r)
				}
			}},
		{name: "sentinel without a ready master is an error", unit: store.Unit{Mode: store.ModeSentinel},
			in: store.Instance{Port: 26379, Kind: "sentinel", Desired: "sentinel"}, host: withAddr, wantErr: "sentinel master is not ready"},
		{name: "data instances carry the unit's modules", unit: store.Unit{Mode: store.ModeStandalone, Modules: "json,bloom"},
			in: store.Instance{Port: 7000, Kind: "server", Desired: "master"}, host: withAddr,
			check: func(t *testing.T, r *agentv1.CreateInstance) {
				if strings.Join(r.Modules, ",") != "json,bloom" {
					t.Fatalf("modules = %v", r.Modules)
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			svc := &Service{CA: ca, License: c.lic}
			u := c.unit
			in := c.in
			p := createInputs{Params: map[string]string{"maxmemory": "64mb"}, MasterAuth: "secret", Masters: c.masters, MasterHosts: map[uint32]*store.Host{}}
			for shard := range c.masters {
				p.MasterHosts[shard] = withAddr
			}
			r, err := svc.createRequest(&u, &in, c.host, p)
			if c.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), c.wantErr) {
					t.Fatalf("err = %v, want it to contain %q", err, c.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Port != in.Port || r.Masterauth != "secret" || r.Params["maxmemory"] != "64mb" {
				t.Fatalf("common fields wrong: %+v", r)
			}
			c.check(t, r)
		})
	}
}

// 调用方传入的参数表不能被请求引用：改请求里的参数不应影响调用方（同一张表给多个实例用）。
func TestCreateRequestCopiesParams(t *testing.T) {
	svc := &Service{}
	params := map[string]string{"a": "1"}
	r, err := svc.createRequest(&store.Unit{Mode: store.ModeStandalone}, &store.Instance{Port: 7000, Kind: "server", Desired: "master"},
		&store.Host{Address: "10.0.0.1"}, createInputs{Params: params})
	if err != nil {
		t.Fatal(err)
	}
	r.Params["a"] = "changed"
	if params["a"] != "1" {
		t.Fatal("request shares the caller's params map")
	}
}

// TLS 已开但控制台 CA 不可用：明确报错，错误信息里不得带出任何密钥材料。
func TestCreateRequestTLSWithoutCA(t *testing.T) {
	svc := &Service{}
	_, err := svc.createRequest(&store.Unit{Mode: store.ModeStandalone, TLS: true}, &store.Instance{Port: 7000, Kind: "server", Desired: "master"},
		&store.Host{Address: "10.0.0.1"}, createInputs{})
	if err == nil || !strings.Contains(err.Error(), "console CA is unavailable") {
		t.Fatalf("err = %v", err)
	}
}
