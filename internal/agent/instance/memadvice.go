package instance

import (
	"fmt"
	"strconv"
	"strings"
)

// 内存建议的阈值（具名常量，便于审计与调整）。
const (
	fragHighRatio        = 1.5      // 碎片率高于此值提示 activedefrag
	fragLowRatio         = 1.0      // 碎片率低于此值提示可能 swap
	fragMinUsedBytes     = 64 << 20 // 碎片建议的已用内存下限（64 MiB），低于此值碎片率没有参考价值
	maxmemoryNearPercent = 90       // 已用内存超过 maxmemory 的此百分比即提示
)

// Advice 是一条基于规则的内存建议。Message 为中文，只引用参数名与数值。
type Advice struct {
	Level   string `json:"level"` // info | warning
	Code    string `json:"code"`
	Message string `json:"message"`
}

// MemoryAdvice 根据已解析的 INFO memory 字段给出建议；纯函数，不访问实例。
// 字段缺失或无法解析时对应规则不触发。
func MemoryAdvice(info map[string]string) []Advice {
	out := []Advice{}
	frag, fragOK := parseFloatField(info, "mem_fragmentation_ratio")
	used, usedOK := parseUintField(info, "used_memory")
	maxmem, maxOK := parseUintField(info, "maxmemory")

	if fragOK && usedOK && frag > fragHighRatio && used >= fragMinUsedBytes {
		out = append(out, Advice{Level: "warning", Code: "frag_high",
			Message: fmt.Sprintf("内存碎片率 %.2f 高于 %.1f（已用内存 %d 字节），可考虑开启参数 activedefrag。", frag, fragHighRatio, used)})
	}
	if fragOK && frag > 0 && frag < fragLowRatio {
		out = append(out, Advice{Level: "warning", Code: "frag_low",
			Message: fmt.Sprintf("内存碎片率 %.2f 低于 %.1f，可能发生了 swap，请检查操作系统的 swap 使用情况。", frag, fragLowRatio)})
	}
	if maxOK && maxmem == 0 {
		out = append(out, Advice{Level: "info", Code: "maxmemory_unset",
			Message: "参数 maxmemory 为 0，实例内存没有上限，请按规格确认是否需要设置。"})
	}
	if maxOK && usedOK && maxmem > 0 && float64(used) > float64(maxmem)*maxmemoryNearPercent/100 {
		policy := info["maxmemory_policy"]
		if policy == "" {
			policy = "未知"
		}
		out = append(out, Advice{Level: "warning", Code: "maxmemory_near",
			Message: fmt.Sprintf("已用内存 %d 字节已超过 maxmemory %d 的 %d%%，当前淘汰策略 maxmemory-policy 为 %s。", used, maxmem, maxmemoryNearPercent, policy)})
	}
	return out
}

func parseFloatField(info map[string]string, key string) (float64, bool) {
	raw, ok := info[key]
	if !ok {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	return f, err == nil
}

func parseUintField(info map[string]string, key string) (uint64, bool) {
	raw, ok := info[key]
	if !ok {
		return 0, false
	}
	n, err := strconv.ParseUint(strings.TrimSpace(raw), 10, 64)
	return n, err == nil
}
