package instance

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/xiaoguan521/cacheos/internal/agent/resp"
)

type decodedReply struct {
	Reply struct {
		Type     string          `json:"type"`
		Value    json.RawMessage `json:"value"`
		Encoding string          `json:"encoding"`
	} `json:"reply"`
	Truncated bool `json:"truncated"`
}

func decodeReply(t *testing.T, b []byte) decodedReply {
	t.Helper()
	var d decodedReply
	if err := json.Unmarshal(b, &d); err != nil {
		t.Fatalf("payload is not JSON: %v: %s", err, b)
	}
	return d
}

func TestEncodeReplyDistinguishesTypes(t *testing.T) {
	cases := []struct {
		name string
		v    resp.Value
		kind string
		want string // value 的 JSON 文本，空表示不检查
	}{
		{"simple", resp.Value{Kind: '+', Str: "OK"}, "simple", `"OK"`},
		{"error", resp.Value{Kind: '-', Str: "NOPERM this user has no permissions"}, "error", `"NOPERM this user has no permissions"`},
		{"integer", resp.Value{Kind: ':', Int: 42}, "integer", `42`},
		{"bulk", resp.Value{Kind: '$', Str: "hi <b>"}, "bulk", `"hi <b>"`},
		{"nil bulk", resp.Value{Kind: '$', Null: true}, "null", ""},
		{"nil array", resp.Value{Kind: '*', Null: true}, "null", ""},
	}
	for _, c := range cases {
		b, kind, err := encodeReply(c.v)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if kind != c.kind {
			t.Errorf("%s: kind %q want %q", c.name, kind, c.kind)
		}
		d := decodeReply(t, b)
		if d.Reply.Type != c.kind || d.Truncated {
			t.Errorf("%s: %s", c.name, b)
		}
		if c.want != "" {
			var got, want any
			if err := json.Unmarshal(d.Reply.Value, &got); err != nil {
				t.Fatal(err)
			}
			_ = json.Unmarshal([]byte(c.want), &want)
			if got != want {
				t.Errorf("%s: value %v want %v", c.name, got, want)
			}
		}
	}
}

func TestEncodeReplyNestedArray(t *testing.T) {
	v := resp.Value{Kind: '*', Array: []resp.Value{
		{Kind: ':', Int: 1},
		{Kind: '*', Array: []resp.Value{{Kind: '$', Str: "a"}, {Kind: '$', Null: true}}},
	}}
	b, kind, err := encodeReply(v)
	if err != nil || kind != "array" {
		t.Fatalf("kind %q err %v", kind, err)
	}
	s := string(b)
	for _, want := range []string{`"type":"array"`, `"type":"integer"`, `"type":"null"`, `"type":"bulk"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s in %s", want, s)
		}
	}
}

func TestEncodeReplyBinaryUsesBase64(t *testing.T) {
	b, _, err := encodeReply(resp.Value{Kind: '$', Str: "\xff\xfe\x00"})
	if err != nil {
		t.Fatal(err)
	}
	d := decodeReply(t, b)
	if d.Reply.Encoding != "base64" || string(d.Reply.Value) != `"//4A"` {
		t.Fatalf("binary reply: %s", b)
	}
}

func TestEncodeReplyTruncatesLargeBulk(t *testing.T) {
	big := strings.Repeat("x", 600<<10)
	b, _, err := encodeReply(resp.Value{Kind: '$', Str: big})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > webCLIMaxPayload {
		t.Fatalf("payload %d bytes exceeds limit", len(b))
	}
	d := decodeReply(t, b)
	if !d.Truncated {
		t.Fatal("truncated flag not set")
	}
	if len(d.Reply.Value) < 100<<10 {
		t.Fatalf("truncation too aggressive: %d bytes kept", len(d.Reply.Value))
	}
}

func TestEncodeReplyTruncatesLargeArray(t *testing.T) {
	items := make([]resp.Value, 50000)
	for i := range items {
		items[i] = resp.Value{Kind: '$', Str: "member-member-member"}
	}
	b, _, err := encodeReply(resp.Value{Kind: '*', Array: items})
	if err != nil {
		t.Fatal(err)
	}
	if len(b) > webCLIMaxPayload {
		t.Fatalf("payload %d bytes exceeds limit", len(b))
	}
	if d := decodeReply(t, b); !d.Truncated {
		t.Fatal("truncated flag not set for large array")
	}
}

func TestEncodeReplyKeepsMultibyteRunesWhole(t *testing.T) {
	b, _, err := encodeReply(resp.Value{Kind: '$', Str: strings.Repeat("缓存", 200000)})
	if err != nil {
		t.Fatal(err)
	}
	d := decodeReply(t, b)
	var s string
	if err := json.Unmarshal(d.Reply.Value, &s); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(s, '�') || !d.Truncated {
		t.Fatalf("multibyte cut badly or not truncated")
	}
}
