package api

import (
	"encoding/json"
	"testing"
)

// 旧版 agent 只回传 report；新版附加 aggregate / advice。两种负载都必须能解出 report，且旧字段形状不变。
func TestDecodeDiagnosePayloadLegacyAndExtended(t *testing.T) {
	legacy := []byte(`{"kind":"SLOWLOG","sample":100,"report":[{"id":1,"command":"GET k"}]}`)
	p, ok := decodeDiagnosePayload(legacy)
	if !ok {
		t.Fatal("legacy payload must decode")
	}
	if p.Report == nil || p.Aggregate != nil || p.Advice != nil {
		t.Fatalf("legacy payload: unexpected fields %+v", p)
	}

	ext := []byte(`{"kind":"SLOWLOG","sample":128,"report":[],"aggregate":[{"command":"HGETALL","count":2,"total_us":10,"avg_us":5,"max_us":6,"last_timestamp":3}],"advice":[{"level":"warning","code":"frag_high","message":"m"}]}`)
	p, ok = decodeDiagnosePayload(ext)
	if !ok {
		t.Fatal("extended payload must decode")
	}
	agg, err := json.Marshal(p.Aggregate)
	if err != nil || string(agg) == "null" {
		t.Fatalf("aggregate not carried through: %s %v", agg, err)
	}
	adv, _ := json.Marshal(p.Advice)
	if string(adv) == "null" {
		t.Fatal("advice not carried through")
	}
}

func TestDecodeDiagnosePayloadRawFallback(t *testing.T) {
	if _, ok := decodeDiagnosePayload([]byte("not json")); ok {
		t.Fatal("non-JSON payload must not decode, caller keeps raw output")
	}
}
