// Package cmdsig 为控制台下发给 agent 的指令做应用层签名与防重放（规格书 5.3、9.2）。
//
// 签名对象是 ServerMessage 去掉 signature 字段后的确定性 protobuf 编码。
package cmdsig

import (
	"crypto/ed25519"
	"errors"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
)

// MaxSkew 是 issued_at 允许的时间偏差。
const MaxSkew = 30 * time.Second

// ReplayWindow 是 cmd_id 去重窗口。
const ReplayWindow = 10 * time.Minute

var (
	ErrSignature = errors.New("cmdsig: bad signature")
	ErrStale     = errors.New("cmdsig: issued_at outside allowed window")
	ErrReplay    = errors.New("cmdsig: replayed cmd_id")
	ErrNoID      = errors.New("cmdsig: missing cmd_id")
)

func payload(m *agentv1.ServerMessage) ([]byte, error) {
	c := proto.Clone(m).(*agentv1.ServerMessage)
	c.Signature = nil
	return proto.MarshalOptions{Deterministic: true}.Marshal(c)
}

// Sign 填写 issued_at 并签名。
func Sign(priv ed25519.PrivateKey, m *agentv1.ServerMessage, now time.Time) error {
	if m.CmdId == "" {
		return ErrNoID
	}
	m.IssuedAtUnix = now.Unix()
	b, err := payload(m)
	if err != nil {
		return err
	}
	m.Signature = ed25519.Sign(priv, b)
	return nil
}

// Verifier 校验签名、时间窗与重放。并发安全。
type Verifier struct {
	Pub  ed25519.PublicKey
	mu   sync.Mutex
	seen map[string]time.Time
}

// Verify 通过时记录 cmd_id；同一 cmd_id 在窗口内再次出现即拒绝。
func (v *Verifier) Verify(m *agentv1.ServerMessage, now time.Time) error {
	if m.CmdId == "" {
		return ErrNoID
	}
	b, err := payload(m)
	if err != nil {
		return err
	}
	if !ed25519.Verify(v.Pub, b, m.Signature) {
		return ErrSignature
	}
	at := time.Unix(m.IssuedAtUnix, 0)
	if at.Before(now.Add(-MaxSkew)) || at.After(now.Add(MaxSkew)) {
		return ErrStale
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.seen == nil {
		v.seen = map[string]time.Time{}
	}
	for id, t := range v.seen {
		if now.Sub(t) > ReplayWindow {
			delete(v.seen, id)
		}
	}
	if _, dup := v.seen[m.CmdId]; dup {
		return ErrReplay
	}
	v.seen[m.CmdId] = now
	return nil
}
