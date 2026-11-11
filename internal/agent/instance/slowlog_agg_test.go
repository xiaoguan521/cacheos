package instance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xiaoguan521/cacheos/internal/agent/resp"
)

func TestAggregateSlowlogByCommandName(t *testing.T) {
	recs := []slowlogRecord{
		{Name: "hgetall", Timestamp: 100, DurationUs: 500},
		{Name: "HGETALL", Timestamp: 200, DurationUs: 1500},
		{Name: " Zrange ", Timestamp: 150, DurationUs: 3000},
		{Name: "zrange", Timestamp: 120, DurationUs: 1000},
		{Name: "", Timestamp: 90, DurationUs: 10},
	}
	got := aggregateSlowlog(recs)
	want := []SlowlogCommandSummary{
		{Command: "ZRANGE", Count: 2, TotalUs: 4000, AvgUs: 2000, MaxUs: 3000, LastTimestamp: 150},
		{Command: "HGETALL", Count: 2, TotalUs: 2000, AvgUs: 1000, MaxUs: 1500, LastTimestamp: 200},
		{Command: "UNKNOWN", Count: 1, TotalUs: 10, AvgUs: 10, MaxUs: 10, LastTimestamp: 90},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d groups, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("group %d: got %+v want %+v", i, got[i], want[i])
		}
	}
}

func TestAggregateSlowlogEmptyInput(t *testing.T) {
	got := aggregateSlowlog(nil)
	if got == nil || len(got) != 0 {
		t.Fatalf("empty input must give empty non-nil slice, got %#v", got)
	}
	b, _ := json.Marshal(got)
	if string(b) != "[]" {
		t.Fatalf("empty aggregate must marshal to [], got %s", b)
	}
}

func TestSlowlogRecordsKeepOnlyCommandName(t *testing.T) {
	const secret = "SUPER-SECRET-VALUE-42"
	entry := resp.Value{Kind: '*', Array: []resp.Value{
		{Kind: ':', Int: 7},
		{Kind: ':', Int: 1700000000},
		{Kind: ':', Int: 2500},
		{Kind: '*', Array: []resp.Value{{Kind: '$', Str: "set"}, {Kind: '$', Str: "user:1"}, {Kind: '$', Str: secret}}},
		{Kind: '$', Str: "10.0.0.1:5555"},
	}}
	recs := slowlogRecords(resp.Value{Kind: '*', Array: []resp.Value{entry}})
	if len(recs) != 1 || recs[0].Name != "set" || recs[0].DurationUs != 2500 || recs[0].Timestamp != 1700000000 {
		t.Fatalf("unexpected records: %+v", recs)
	}
	b, err := json.Marshal(aggregateSlowlog(recs))
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{secret, "user:1", "10.0.0.1"} {
		if strings.Contains(string(b), bad) {
			t.Fatalf("aggregate leaks argument/client %q: %s", bad, b)
		}
	}
}
