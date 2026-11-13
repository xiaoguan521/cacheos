// Package webcli 是 Web 命令行（CLAUDE.md「agent 与安全」中唯一的例外）的 argv 校验，
// 控制台与 agent 共用同一份实现，避免两边各写一套清单。
//
// 这里只做纯校验：不连接实例、不做网络 IO。引擎侧的 ACL 另行拒绝同一批命令（见 internal/agent/instance/render.go）。
package webcli

import (
	"regexp"
	"strings"
)

// 错误码（与 HTTP 响应、CommandResult.error_code 共用）。
const (
	CodeInvalid = "CLI_ARGV_INVALID"   // argv 结构或命令名不合法
	CodeDenied  = "CLI_COMMAND_DENIED" // 命中拒绝清单
)

// 参数上限。
const (
	MaxArgs       = 64
	MaxArgBytes   = 64 << 10  // 单个参数 64 KiB
	MaxTotalBytes = 256 << 10 // 所有参数合计 256 KiB
)

// Error 是校验失败的错误，Code 为上面的常量之一。
type Error struct {
	Code string
	Msg  string
}

func (e *Error) Error() string { return e.Code + ": " + e.Msg }

// nameRe 命令名：字母开头，只含字母、数字、点、下划线与横线，最长 64。
var nameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]{0,63}$`)

// DeniedCommands 是拒绝清单（命令名，包含其全部子命令），比较时转为大写。
// 有序切片，测试遍历它保证每一条都有用例。
// 刻意不在此清单中的：COMMAND、LASTSAVE、PUBLISH、SPUBLISH（由 ACL 决定是否可用）。
// XREAD / XREADGROUP 只在带 BLOCK 参数时拒绝，见 blockingFlagCommands。
var DeniedCommands = []string{
	"ACL", "AUTH", "BGREWRITEAOF", "BGSAVE", "CLIENT", "CLUSTER", "CONFIG", "DEBUG", "DISCARD",
	"EVAL", "EVALSHA", "EVAL_RO", "EVALSHA_RO", "EXEC", "FAILOVER", "FCALL", "FCALL_RO",
	"FLUSHALL", "FLUSHDB", "FUNCTION", "HELLO", "KEYS", "LATENCY", "MIGRATE", "MODULE", "MONITOR",
	"MULTI", "PFDEBUG", "PFSELFTEST", "PSUBSCRIBE", "PSYNC", "QUIT", "READONLY", "READWRITE",
	"REPLCONF", "REPLICAOF", "RESET", "RESTORE", "RESTORE-ASKING", "ROLE", "SAVE", "SCRIPT", "SELECT",
	"SHUTDOWN", "SLAVEOF", "SLOWLOG", "SORT", "SSUBSCRIBE", "SUBSCRIBE", "SWAPDB", "SYNC",
	"UNSUBSCRIBE", "PUNSUBSCRIBE", "SUNSUBSCRIBE", "UNWATCH", "WAIT", "WAITAOF", "WATCH",
	// 扩展模块命令（ADR 0002）：调试、内部与整体加载类
	"JSON.DEBUG", "BF.LOAD", "FT._DEBUG", "FT._LIST", "FT.INTERNAL_UPDATE",
	// 阻塞命令
	"BLPOP", "BRPOP", "BRPOPLPUSH", "BLMOVE", "BLMPOP", "BZPOPMIN", "BZPOPMAX", "BZMPOP",
}

// blockingFlagCommands 只有带 BLOCK 参数时才拒绝。
var blockingFlagCommands = []string{"XREAD", "XREADGROUP"}

var denied, blockingFlag map[string]bool

func init() {
	denied = make(map[string]bool, len(DeniedCommands))
	for _, c := range DeniedCommands {
		denied[c] = true
	}
	blockingFlag = make(map[string]bool, len(blockingFlagCommands))
	for _, c := range blockingFlagCommands {
		blockingFlag[c] = true
	}
}

// Check 校验 argv，通过时返回大写的命令名。argv[0] 为命令名。
// 注意：参数值不会出现在返回的错误信息中。
func Check(argv []string) (string, error) {
	if len(argv) == 0 {
		return "", &Error{CodeInvalid, "argv 为空"}
	}
	if len(argv) > MaxArgs {
		return "", &Error{CodeInvalid, "参数个数超过上限"}
	}
	total := 0
	for _, a := range argv {
		if len(a) > MaxArgBytes {
			return "", &Error{CodeInvalid, "单个参数超过 64 KiB"}
		}
		total += len(a)
	}
	if total > MaxTotalBytes {
		return "", &Error{CodeInvalid, "参数总长超过 256 KiB"}
	}
	if !nameRe.MatchString(argv[0]) {
		return "", &Error{CodeInvalid, "命令名不合法"}
	}
	name := strings.ToUpper(argv[0])
	if denied[name] {
		return "", &Error{CodeDenied, "命令 " + name + " 不允许在 Web 命令行中执行"}
	}
	if blockingFlag[name] {
		for _, a := range argv[1:] {
			if strings.EqualFold(a, "BLOCK") {
				return "", &Error{CodeDenied, "命令 " + name + " 带 BLOCK 参数时不允许在 Web 命令行中执行"}
			}
		}
	}
	return name, nil
}

// CommandName 返回通过命令名格式校验的大写命令名，否则返回空串（用于审计与日志，永不返回原始输入）。
func CommandName(argv []string) string {
	if len(argv) == 0 || !nameRe.MatchString(argv[0]) {
		return ""
	}
	return strings.ToUpper(argv[0])
}
