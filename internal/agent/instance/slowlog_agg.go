package instance

import (
	"sort"
	"strings"

	"github.com/xiaoguan521/cacheos/internal/agent/resp"
)

// slowlogDiagSample 是诊断 SLOWLOG 时一次拉取的条数上限（SLOWLOG GET 的参数）。
const slowlogDiagSample = 128

// SlowlogCommandSummary 是按命令名聚合的慢日志统计。聚合键只有命令名，不含任何参数或客户端信息。
type SlowlogCommandSummary struct {
	Command       string `json:"command"`
	Count         uint64 `json:"count"`
	TotalUs       int64  `json:"total_us"`
	AvgUs         int64  `json:"avg_us"`
	MaxUs         int64  `json:"max_us"`
	LastTimestamp int64  `json:"last_timestamp"` // 最近一次出现的 Unix 秒
}

// slowlogRecord 是一条慢日志中聚合需要的最小字段；Name 只取命令名（参数数组的第一个元素）。
type slowlogRecord struct {
	Name       string
	Timestamp  int64
	DurationUs int64
}

// slowlogRecords 从 SLOWLOG GET 的应答中取出命令名、时间与耗时，丢弃全部参数与客户端地址。
func slowlogRecords(v resp.Value) []slowlogRecord {
	out := make([]slowlogRecord, 0, len(v.Array))
	for _, e := range v.Array {
		if len(e.Array) < 4 {
			continue
		}
		name := ""
		if args := e.Array[3].Strings(); len(args) > 0 {
			name = args[0]
		}
		out = append(out, slowlogRecord{Name: name, Timestamp: e.Array[1].Int, DurationUs: e.Array[2].Int})
	}
	return out
}

// aggregateSlowlog 按命令名（大小写与首尾空白归一）分组，结果按总耗时降序（同耗时按名称升序）。
// 空命令名归为 UNKNOWN。输入为空时返回非 nil 的空切片。
func aggregateSlowlog(recs []slowlogRecord) []SlowlogCommandSummary {
	byName := map[string]*SlowlogCommandSummary{}
	for _, r := range recs {
		name := strings.ToUpper(strings.TrimSpace(r.Name))
		if name == "" {
			name = "UNKNOWN"
		}
		s := byName[name]
		if s == nil {
			s = &SlowlogCommandSummary{Command: name}
			byName[name] = s
		}
		s.Count++
		s.TotalUs += r.DurationUs
		if r.DurationUs > s.MaxUs {
			s.MaxUs = r.DurationUs
		}
		if r.Timestamp > s.LastTimestamp {
			s.LastTimestamp = r.Timestamp
		}
	}
	out := make([]SlowlogCommandSummary, 0, len(byName))
	for _, s := range byName {
		s.AvgUs = s.TotalUs / int64(s.Count)
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TotalUs != out[j].TotalUs {
			return out[i].TotalUs > out[j].TotalUs
		}
		return out[i].Command < out[j].Command
	})
	return out
}
