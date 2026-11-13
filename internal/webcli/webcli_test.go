package webcli

import (
	"errors"
	"strings"
	"testing"
)

// 与 CLAUDE.md 约定逐条对照的拒绝清单（改动清单时必须同步改这里，避免静默删除）。
var wantDenied = []string{"ACL", "AUTH", "BGREWRITEAOF", "BGSAVE", "CLIENT", "CLUSTER", "CONFIG", "DEBUG", "DISCARD", "EVAL",
	"EVALSHA", "EVAL_RO", "EVALSHA_RO", "EXEC", "FAILOVER", "FCALL", "FCALL_RO", "FLUSHALL", "FLUSHDB", "FUNCTION", "HELLO",
	"KEYS", "LATENCY", "MIGRATE", "MODULE", "MONITOR", "MULTI", "PFDEBUG", "PFSELFTEST", "PSUBSCRIBE", "PSYNC", "QUIT",
	"READONLY", "READWRITE", "REPLCONF", "REPLICAOF", "RESET", "RESTORE", "RESTORE-ASKING", "ROLE", "SAVE", "SCRIPT", "SELECT",
	"SHUTDOWN", "SLAVEOF", "SLOWLOG", "SORT", "SSUBSCRIBE", "SUBSCRIBE", "SWAPDB", "SYNC", "UNSUBSCRIBE", "PUNSUBSCRIBE",
	"SUNSUBSCRIBE", "UNWATCH", "WAIT", "WAITAOF", "WATCH",
	"JSON.DEBUG", "BF.LOAD", "FT._DEBUG", "FT._LIST", "FT.INTERNAL_UPDATE",
	"BLPOP", "BRPOP", "BRPOPLPUSH", "BLMOVE", "BLMPOP", "BZPOPMIN", "BZPOPMAX", "BZMPOP"}

func TestDeniedListMatchesPolicy(t *testing.T) {
	got := map[string]bool{}
	for _, c := range DeniedCommands {
		if got[c] {
			t.Errorf("duplicate entry %s", c)
		}
		got[c] = true
	}
	for _, c := range wantDenied {
		if !got[c] {
			t.Errorf("denylist lacks %s", c)
		}
	}
	if len(got) != len(wantDenied) {
		t.Errorf("denylist has %d entries, policy lists %d", len(got), len(wantDenied))
	}
	// 提醒项：这些命令不在静态拒绝清单中（XREAD / XREADGROUP 只在带 BLOCK 时拒绝）。
	for _, c := range []string{"COMMAND", "LASTSAVE", "PUBLISH", "SPUBLISH", "XREAD", "XREADGROUP"} {
		if got[c] {
			t.Errorf("%s must not be in the static denylist", c)
		}
	}
}

// 拒绝清单中每条命令都必须有用例：大写、小写、大小写混合、带子命令参数。
func TestEveryDeniedCommandIsRejected(t *testing.T) {
	for _, c := range DeniedCommands {
		for _, name := range []string{c, strings.ToLower(c), mixedCase(c)} {
			for _, argv := range [][]string{{name}, {name, "a"}, {name, "GET", "x"}} {
				_, err := Check(argv)
				if err == nil {
					t.Errorf("%v was accepted", argv)
					continue
				}
				if !isCode(err, CodeDenied) {
					t.Errorf("%v rejected with %v, want %s", argv, err, CodeDenied)
				}
			}
		}
	}
}

func mixedCase(s string) string {
	b := []byte(strings.ToLower(s))
	for i := range b {
		if i%2 == 1 && b[i] >= 'a' && b[i] <= 'z' {
			b[i] -= 'a' - 'A'
		}
	}
	return string(b)
}

func TestBlockingFlagOnStreamReads(t *testing.T) {
	for _, argv := range [][]string{
		{"XREAD", "BLOCK", "0", "STREAMS", "s", "$"},
		{"xread", "count", "1", "block", "100", "streams", "s", "$"},
		{"XREADGROUP", "GROUP", "g", "c", "BLOCK", "0", "STREAMS", "s", ">"},
	} {
		if _, err := Check(argv); !isCode(err, CodeDenied) {
			t.Errorf("%v with BLOCK: %v", argv, err)
		}
	}
	for _, argv := range [][]string{
		{"XREAD", "COUNT", "10", "STREAMS", "s", "0"},
		{"XREADGROUP", "GROUP", "g", "c", "STREAMS", "s", ">"},
	} {
		if _, err := Check(argv); err != nil {
			t.Errorf("%v without BLOCK rejected: %v", argv, err)
		}
	}
}

func TestAllowedDataCommands(t *testing.T) {
	cases := map[string][]string{
		"GET":      {"GET", "k"},
		"set":      {"set", "k", "v"},
		"HGETALL":  {"HGETALL", "h"},
		"LASTSAVE": {"LASTSAVE"},
		"COMMAND":  {"COMMAND", "COUNT"},
		"PUBLISH":  {"PUBLISH", "ch", "msg"},
		"SPUBLISH": {"SPUBLISH", "ch", "msg"},
		"DBSIZE":   {"dbsize"},
		"EXPIRE":   {"EXPIRE", "k", "10"},
		"CONFIGX":  {"CONFIGX"}, // 只按整个命令名匹配，不做前缀匹配
		"SET.ex-1": {"SET.ex-1", "k", "v"},
	}
	for name, argv := range cases {
		if _, err := Check(argv); err != nil {
			t.Errorf("%s rejected: %v", name, err)
		}
	}
	// 参数值可以包含空白、换行与 CRLF，它们以 bulk string 发送，不影响命令名。
	if _, err := Check([]string{"SET", "k", "a\r\nb c\n"}); err != nil {
		t.Errorf("value with CRLF rejected: %v", err)
	}
}

func TestCheckRejectsInvalidShapes(t *testing.T) {
	bigArg := strings.Repeat("x", MaxArgBytes+1)
	cases := map[string][]string{
		"empty":           {},
		"nil":             nil,
		"space in name":   {"GE T", "k"},
		"newline in name": {"GET\nFLUSHALL"},
		"crlf in name":    {"GET\r\nFLUSHALL"},
		"leading dash":    {"-GET", "k"},
		"leading digit":   {"1GET"},
		"empty name":      {""},
		"pipe in name":    {"CONFIG|GET", "maxmemory"},
		"slash in name":   {"GET/x"},
		"name too long":   {strings.Repeat("A", 65)},
		"oversize arg":    {"SET", "k", bigArg},
		"too many args":   append([]string{"GET"}, make([]string, MaxArgs)...),
	}
	for name, argv := range cases {
		if _, err := Check(argv); !isCode(err, CodeInvalid) {
			t.Errorf("%s: %v, want %s", name, err, CodeInvalid)
		}
	}
}

func TestCheckLimits(t *testing.T) {
	// 恰好 64 个参数（含命令名）通过。
	argv := []string{"GET"}
	for len(argv) < MaxArgs {
		argv = append(argv, "k")
	}
	if _, err := Check(argv); err != nil {
		t.Errorf("64 args rejected: %v", err)
	}
	// 单个参数恰好 64 KiB 通过。
	if _, err := Check([]string{"SET", "k", strings.Repeat("x", MaxArgBytes)}); err != nil {
		t.Errorf("64 KiB arg rejected: %v", err)
	}
	// 总长恰好 256 KiB 通过，超过 1 字节失败。
	per := MaxArgBytes
	exact := []string{"SET", strings.Repeat("x", per), strings.Repeat("x", per), strings.Repeat("x", per), strings.Repeat("x", MaxTotalBytes-3*per-3)}
	if _, err := Check(exact); err != nil {
		t.Errorf("256 KiB total rejected: %v", err)
	}
	if _, err := Check(append(exact, "x")); !isCode(err, CodeInvalid) {
		t.Errorf("total over 256 KiB accepted: %v", err)
	}
}

func TestCommandNameNormalized(t *testing.T) {
	if got := CommandName([]string{"get", "k"}); got != "GET" {
		t.Errorf("CommandName = %q", got)
	}
	if got := CommandName([]string{"GE T"}); got != "" {
		t.Errorf("invalid name leaked: %q", got)
	}
	if got := CommandName(nil); got != "" {
		t.Errorf("empty argv gave %q", got)
	}
}

func isCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}
