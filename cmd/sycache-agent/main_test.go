package main

import (
	"os"
	"path/filepath"
	"testing"
)

// node.json 由 agent 自己写（缩进 JSON），但读取不能依赖具体排版：压缩格式、字段顺序、
// 转义字符都应该得到同样的 server 地址；坏文件得到空串。
func TestServerFromStateReadsJSONNotText(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"indented", "{\n  \"host_id\": \"h1\",\n  \"server\": \"console.example:9443\",\n  \"command_pub\": \"AAAA\"\n}", "console.example:9443"},
		{"compact", `{"host_id":"h1","server":"console.example:9443"}`, "console.example:9443"},
		{"server first", `{"server":"10.0.0.5:9443","host_id":"h1"}`, "10.0.0.5:9443"},
		{"escaped", `{"server":"a-b:9443"}`, "a-b:9443"},
		{"server text inside another field", `{"host_id":"\"server\": \"evil:1\"","server":"real:9443"}`, "real:9443"},
		{"not json", `server: console:9443`, ""},
		{"no server", `{"host_id":"h1"}`, ""},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "node.json"), []byte(c.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := serverFromState(dir); got != c.want {
			t.Errorf("%s: serverFromState = %q, want %q", c.name, got, c.want)
		}
	}
	if got := serverFromState(t.TempDir()); got != "" {
		t.Errorf("missing file: got %q, want empty", got)
	}
}
