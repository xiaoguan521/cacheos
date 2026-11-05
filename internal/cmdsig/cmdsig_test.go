package cmdsig

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"testing"
	"time"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
)

func msg(id string) *agentv1.ServerMessage {
	return &agentv1.ServerMessage{CmdId: id, Body: &agentv1.ServerMessage_Params{Params: &agentv1.ApplyParams{Port: 7001, RevisionId: "p00000002", ParentRevisionId: "p00000001", Params: map[string]string{"hz": "20"}}}}
}

func TestSignVerify(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	now := time.Now()
	v := &Verifier{Pub: pub}

	m := msg("c1")
	if err := Sign(priv, m, now); err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(m, now); err != nil {
		t.Fatal(err)
	}
	if err := v.Verify(m, now.Add(time.Second)); !errors.Is(err, ErrReplay) {
		t.Fatalf("replay: %v", err)
	}

	m2 := msg("c2")
	_ = Sign(priv, m2, now)
	m2.GetParams().Params["hz"] = "500" // 篡改
	if err := v.Verify(m2, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("tamper: %v", err)
	}

	m3 := msg("c3")
	_ = Sign(priv, m3, now.Add(-time.Minute))
	if err := v.Verify(m3, now); !errors.Is(err, ErrStale) {
		t.Fatalf("stale: %v", err)
	}

	_, other, _ := ed25519.GenerateKey(rand.Reader)
	m4 := msg("c4")
	_ = Sign(other, m4, now)
	if err := v.Verify(m4, now); !errors.Is(err, ErrSignature) {
		t.Fatalf("wrong key: %v", err)
	}
}
