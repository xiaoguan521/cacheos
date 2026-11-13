package orch

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
	"github.com/xiaoguan521/cacheos/internal/console/store"
	"github.com/xiaoguan521/cacheos/internal/license"
)

// createInputs 是生成创建请求时调用方才知道的东西：本单元的参数、复制/集群口令、业务账号，
// 以及“已就绪的主库”。主库的来源由调用方决定（创建时是本轮刚建好的，扩容时是库里已有的），
// masterHosts 允许缺项：缺了就按主库的 HostID 查一次并写回。
type createInputs struct {
	Params      map[string]string
	MasterAuth  string
	Users       []*agentv1.AclUser
	Masters     map[uint32]*store.Instance
	MasterHosts map[uint32]*store.Host
}

// createRequest 为部署单元 u 的实例 in（落在主机 h 上）生成下发给 agent 的创建请求：
// 监听地址、形态、模块、数据面 TLS（含内部 TLS 的许可判断）、副本与哨兵的接线都在这里。
// 出错时由调用方决定如何处置：创建整体失败，扩容则暂停。本函数不记录请求内容（含私钥与口令）。
func (s *Service) createRequest(u *store.Unit, in *store.Instance, h *store.Host, p createInputs) (*agentv1.CreateInstance, error) {
	params := make(map[string]string, len(p.Params))
	for k, v := range p.Params {
		params[k] = v
	}
	kind := agentv1.Kind_KIND_SERVER
	if in.Kind == "sentinel" {
		kind = agentv1.Kind_KIND_SENTINEL
	}
	// 业务监听地址取主机的内部地址；agent 写数据面配置前还会独立再校验一次。
	req := &agentv1.CreateInstance{Port: in.Port, Kind: kind, Params: params, RevisionId: u.RevisionID, Masterauth: p.MasterAuth, AclUsers: p.Users,
		ClusterEnabled: u.Mode == store.ModeCluster, BindAddress: hostAddress(h), Modules: requestModules(u, kind)}
	if u.TLS && in.Kind == "server" {
		tls, err := s.dataPlaneTLS(u, h)
		if err != nil {
			return nil, err
		}
		req.Tls = tls
	}
	switch {
	case in.Kind == "sentinel":
		master, host, err := s.readyMaster(p, 0)
		if err != nil {
			return nil, errors.New("sentinel master is not ready")
		}
		req.SentinelMonitorName = u.SentinelName
		req.SentinelMasterHost = hostAddress(host)
		req.SentinelMasterPort = master.Port
		req.SentinelQuorum = u.Quorum
	case in.Desired == "replica" && u.Mode != store.ModeCluster:
		master, host, err := s.readyMaster(p, in.Shard)
		if err != nil {
			return nil, err
		}
		req.ReplicaOf = &agentv1.ReplicaOf{Host: hostAddress(host), Port: master.Port}
	}
	return req, nil
}

// readyMaster 取某分片已就绪的主库及其主机；masterHosts 缺项时查询并写回。
func (s *Service) readyMaster(p createInputs, shard uint32) (*store.Instance, *store.Host, error) {
	master := p.Masters[shard]
	if master == nil {
		return nil, nil, fmt.Errorf("master for shard %d is not ready", shard)
	}
	host := p.MasterHosts[shard]
	if host == nil {
		var err error
		if host, err = s.host(master.HostID); err != nil {
			return nil, nil, err
		}
		if p.MasterHosts != nil {
			p.MasterHosts[shard] = host
		}
	}
	return master, host, nil
}

// dataPlaneTLS 为数据实例签发短期服务证书；内部 TLS（复制、集群总线）要求许可含对应功能。
func (s *Service) dataPlaneTLS(u *store.Unit, h *store.Host) (*agentv1.TlsMaterial, error) {
	if s.CA == nil {
		return nil, errors.New("data-plane TLS is enabled but the console CA is unavailable")
	}
	cert, err := s.CA.ServerCert([]string{h.Address, h.Hostname, "localhost"})
	if err != nil {
		return nil, fmt.Errorf("issue data-plane TLS certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		return nil, fmt.Errorf("encode data-plane TLS key: %w", err)
	}
	internal := s.License != nil && s.License().Feature(license.FeatureTLSInternal)
	return &agentv1.TlsMaterial{
		CertPem:        pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]}),
		KeyPem:         pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		CaPem:          append([]byte(nil), s.CA.PEM...),
		TlsReplication: internal,
		TlsCluster:     internal && u.Mode == store.ModeCluster,
		AuthClients:    false,
	}, nil
}
