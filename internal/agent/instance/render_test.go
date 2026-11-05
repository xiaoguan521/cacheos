package instance

import (
	"strings"
	"testing"

	agentv1 "github.com/xiaoguan521/cacheos/api/gen/agent/v1"
)

// agent 管理账号不得含任何读取 value 的命令（CLAUDE.md「agent 与安全」）。
func TestAgentRulesHaveNoValueReads(t *testing.T) {
	banned := []string{"+@all", "+@read", "+@string", "+@hash", "+@list", "+@set", "+@sortedset", "+@stream", "+get", "+mget",
		"+hgetall", "+hget", "+lrange", "+smembers", "+zrange", "+xrange", "+dump", "+eval", "+evalsha", "+fcall", "+@scripting",
		"+keys", "+sort", "+getrange", "+debug", "+module", "+monitor", "+sync", "+psync", "+getdel", "+getex"}
	joined := " " + strings.Join(AgentRules, " ") + " "
	for _, b := range banned {
		if strings.Contains(joined, " "+b+" ") {
			t.Errorf("agent rules grant %s", b)
		}
	}
	if AgentRules[2] != "-@all" {
		t.Fatal("agent rules must start from -@all")
	}
}

func TestValidateUser(t *testing.T) {
	h := strings.Repeat("a", 64)
	ok := &agentv1.AclUser{Name: "app1", PasswordSha256: []string{h}, Rules: []string{"~app:*", "+@read", "+@write", "-@dangerous", "&news.*", "%R~cache:*"}, Enabled: true}
	if err := ValidateUser(ok); err != nil {
		t.Fatal(err)
	}
	for _, u := range []*agentv1.AclUser{
		{Name: "default", PasswordSha256: []string{h}, Enabled: true},
		{Name: "sycache-agent", PasswordSha256: []string{h}, Enabled: true},
		{Name: "app 1", PasswordSha256: []string{h}, Enabled: true},
		{Name: "app\nuser x on", PasswordSha256: []string{h}, Enabled: true},
		{Name: "app", Enabled: true},
		{Name: "app", PasswordSha256: []string{"short"}, Enabled: true},
		{Name: "app", PasswordSha256: []string{h}, Rules: []string{">plaintext"}, Enabled: true},
		{Name: "app", PasswordSha256: []string{h}, Rules: []string{"nopass"}, Enabled: true},
		{Name: "app", PasswordSha256: []string{h}, Rules: []string{"+@all\nuser evil on nopass +@all"}, Enabled: true},
		{Name: "app", PasswordSha256: []string{h}, Rules: []string{"+get +set"}, Enabled: true},
		{Name: "app", PasswordSha256: []string{h}, Rules: []string{"~a b"}, Enabled: true},
	} {
		if err := ValidateUser(u); err == nil {
			t.Errorf("accepted %q %q", u.Name, u.Rules)
		}
	}
}

func TestServerConf(t *testing.T) {
	l := Layout{Root: "/opt/sycache", RunDir: "/run/sycache", LogDir: "/var/log/sycache"}
	st := &State{Port: 7001, Revision: "p00000001", Params: map[string]string{"maxmemory": "1073741824", "save": "", "maxmemory-policy": "allkeys-lru"}, AdminPassword: "x"}
	conf := l.ServerConf(st, &agentv1.CreateInstance{ReplicaOf: &agentv1.ReplicaOf{Host: "10.0.0.1", Port: 7001}, Masterauth: "s3cret"})
	for _, want := range []string{"port 7001\n", "unixsocket /run/sycache/sycache-7001.sock\n", "dir /opt/sycache/instances/7001\n",
		"aclfile /opt/sycache/instances/7001/users.acl\n", "replicaof 10.0.0.1 7001\n", "masteruser sycache-repl\n", "masterauth s3cret\n",
		"maxmemory 1073741824\n", "save \"\"\n", "maxmemory-policy allkeys-lru\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("missing %q in\n%s", want, conf)
		}
	}
	acl := ACLFile(st, "s3cret", nil)
	if strings.Contains(acl, "s3cret") || strings.Contains(acl, " x ") || !strings.HasPrefix(acl, "user default off") {
		t.Fatalf("acl file leaks secrets or enables default:\n%s", acl)
	}
}

func TestSetConfLines(t *testing.T) {
	in := "port 7001\nsave 3600 1\nsave 300 100\ndatabases 16\n"
	out := SetConfLines(in, map[string]string{"databases": "32", "save": "", "io-threads": "4"})
	want := "port 7001\nsave \"\"\ndatabases 32\nio-threads 4\n"
	if out != want {
		t.Fatalf("got %q want %q", out, want)
	}
}
