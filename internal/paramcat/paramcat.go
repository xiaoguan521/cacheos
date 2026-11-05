// Package paramcat 是 sycache 的参数目录（CacheOS 规格书 6.3、8 节 M5）。
//
// 参数的类型、范围、默认值与是否需重启由 scripts/gen-paramcat.py 从上游 src/config.c 生成（catalog_gen.go）；
// 只有目录中的参数可以经控制台修改。端口、目录、口令、TLS、复制关系等由编排自动生成，不在目录中。
// 控制台与 agent 共用本包：控制台在生成参数版本前校验，agent 在执行前再校验一次。
package paramcat

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Param 是一个可经控制台修改的参数。
type Param struct {
	Name    string   `json:"name"`
	Alias   string   `json:"alias,omitempty"`
	Group   string   `json:"group"`
	Type    string   `json:"type"` // bool | int | enum | string | special
	Restart bool     `json:"restart"`
	Default string   `json:"default"`
	Min     int64    `json:"min,omitempty"`
	Max     int64    `json:"max,omitempty"`
	Memory  bool     `json:"memory,omitempty"`  // 接受 1gb / 512mb 这类写法
	Percent bool     `json:"percent,omitempty"` // 接受 10% 写法
	Enum    []string `json:"enum,omitempty"`
	Format  string   `json:"format,omitempty"` // special 参数的校验方式
}

var byName = func() map[string]*Param {
	m := map[string]*Param{}
	for i := range catalog {
		m[catalog[i].Name] = &catalog[i]
	}
	return m
}()

// All 返回全部参数（按目录顺序）。
func All() []Param { return append([]Param(nil), catalog...) }

// Lookup 按名称查找参数；接受别名。
func Lookup(name string) (*Param, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	if p, ok := byName[name]; ok {
		return p, true
	}
	for i := range catalog {
		if catalog[i].Alias == name {
			return &catalog[i], true
		}
	}
	return nil, false
}

// ErrUnknown 表示参数不在目录中（不允许经控制台修改）。
var ErrUnknown = errors.New("parameter is not managed by the console")

var memRe = regexp.MustCompile(`^(\d+)(b|k|kb|m|mb|g|gb)?$`)

// ParseMemory 解析内存写法（与上游 memtoull 一致：k=1000、kb=1024，以此类推）。
func ParseMemory(v string) (int64, error) {
	m := memRe.FindStringSubmatch(strings.ToLower(strings.TrimSpace(v)))
	if m == nil {
		return 0, fmt.Errorf("invalid memory value %q", v)
	}
	n, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		return 0, err
	}
	mul := map[string]int64{"": 1, "b": 1, "k": 1000, "kb": 1024, "m": 1000 * 1000, "mb": 1 << 20, "g": 1000 * 1000 * 1000, "gb": 1 << 30}[m[2]]
	if n > (1<<62)/mul {
		return 0, fmt.Errorf("memory value %q too large", v)
	}
	return n * mul, nil
}

var (
	saveRe   = regexp.MustCompile(`^(\d+ \d+)( \d+ \d+)*$`)
	eventsRe = regexp.MustCompile(`^[KEg$lshzxetdmnA]*$`)
	cobRe    = regexp.MustCompile(`^(normal|replica|slave|pubsub) \S+ \S+ \d+$`)
)

// Normalize 校验并规范化一个参数值；返回写入配置的值。
func Normalize(name, value string) (string, error) {
	p, ok := Lookup(name)
	if !ok {
		return "", fmt.Errorf("%s: %w", name, ErrUnknown)
	}
	v := strings.TrimSpace(value)
	if strings.ContainsAny(v, "\r\n\x00\"'\\") {
		return "", fmt.Errorf("%s: value contains forbidden characters", p.Name)
	}
	switch p.Type {
	case "bool":
		switch strings.ToLower(v) {
		case "yes", "true", "1", "on":
			return "yes", nil
		case "no", "false", "0", "off":
			return "no", nil
		}
		return "", fmt.Errorf("%s: expected yes or no", p.Name)
	case "enum":
		for _, e := range p.Enum {
			if strings.EqualFold(e, v) {
				return e, nil
			}
		}
		return "", fmt.Errorf("%s: must be one of %s", p.Name, strings.Join(p.Enum, ", "))
	case "int":
		var n int64
		var err error
		switch {
		case p.Percent && strings.HasSuffix(v, "%"):
			pct, perr := strconv.ParseInt(strings.TrimSuffix(v, "%"), 10, 64)
			if perr != nil || pct < 0 || pct > 100 {
				return "", fmt.Errorf("%s: invalid percentage %q", p.Name, v)
			}
			return v, nil
		case p.Memory:
			n, err = ParseMemory(v)
		default:
			n, err = strconv.ParseInt(v, 10, 64)
		}
		if err != nil {
			return "", fmt.Errorf("%s: %v", p.Name, err)
		}
		if n < p.Min || (p.Max != 0 && n > p.Max) {
			return "", fmt.Errorf("%s: %d out of range [%d, %d]", p.Name, n, p.Min, p.Max)
		}
		return strconv.FormatInt(n, 10), nil
	case "special":
		switch p.Format {
		case "save":
			if v == "" || saveRe.MatchString(v) {
				return v, nil
			}
			return "", fmt.Errorf("%s: expected pairs of <seconds> <changes>, or empty to disable", p.Name)
		case "keyspace-events":
			if eventsRe.MatchString(v) {
				return v, nil
			}
			return "", fmt.Errorf("%s: invalid event classes %q", p.Name, v)
		case "client-output-buffer-limit":
			f := strings.Fields(v)
			if len(f)%4 != 0 || len(f) == 0 {
				return "", fmt.Errorf("%s: expected groups of <class> <hard> <soft> <seconds>", p.Name)
			}
			for i := 0; i < len(f); i += 4 {
				if !cobRe.MatchString(strings.Join(f[i:i+4], " ")) {
					return "", fmt.Errorf("%s: invalid group %q", p.Name, strings.Join(f[i:i+4], " "))
				}
				for _, m := range f[i+1 : i+3] {
					if _, err := ParseMemory(m); err != nil {
						return "", fmt.Errorf("%s: %v", p.Name, err)
					}
				}
			}
			return strings.Join(f, " "), nil
		}
		return "", fmt.Errorf("%s: unsupported special parameter", p.Name)
	default:
		if len(v) > 256 {
			return "", fmt.Errorf("%s: value too long", p.Name)
		}
		return v, nil
	}
}

// NormalizeAll 校验一组参数，返回规范化结果（按参数名规范为正式名称）。
func NormalizeAll(in map[string]string) (map[string]string, error) {
	out := make(map[string]string, len(in))
	var errs []string
	for k, v := range in {
		p, ok := Lookup(k)
		if !ok {
			errs = append(errs, fmt.Sprintf("%s: %v", k, ErrUnknown))
			continue
		}
		nv, err := Normalize(p.Name, v)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		out[p.Name] = nv
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return nil, errors.New(strings.Join(errs, "; "))
	}
	return out, nil
}

// Diff 返回从 old 到 new 变化的参数，并拆分为可热改与需重启两组（规格书 6.3 第 2 步）。
func Diff(old, new map[string]string) (changed map[string]string, hot, restart []string) {
	changed = map[string]string{}
	for k, v := range new {
		if old[k] != v {
			changed[k] = v
		}
	}
	for k := range old {
		if _, ok := new[k]; !ok {
			if p, ok := Lookup(k); ok {
				changed[k] = p.Default // 删除即恢复默认值
			}
		}
	}
	for k := range changed {
		if p, ok := Lookup(k); ok && p.Restart {
			restart = append(restart, k)
		} else {
			hot = append(hot, k)
		}
	}
	sort.Strings(hot)
	sort.Strings(restart)
	return changed, hot, restart
}

// Equal 比较实例回读值与期望值是否等价（服务端会把 1gb 返回为字节数、把事件类别重新排序）。
func Equal(name, actual, want string) bool {
	if actual == want {
		return true
	}
	p, ok := Lookup(name)
	if !ok {
		return false
	}
	switch {
	case p.Format == "keyspace-events":
		return eventSet(actual) == eventSet(want)
	case p.Format == "client-output-buffer-limit":
		// 期望值可能只含部分类别：逐组比较（内存写法换算成字节）
		have := strings.Fields(actual)
		wf := strings.Fields(want)
		for i := 0; i+3 < len(wf); i += 4 {
			cls := wf[i]
			if cls == "slave" {
				cls = "replica"
			}
			found := false
			for j := 0; j+3 < len(have); j += 4 {
				hc := have[j]
				if hc == "slave" {
					hc = "replica"
				}
				if hc == cls && memEq(have[j+1], wf[i+1]) && memEq(have[j+2], wf[i+2]) && have[j+3] == wf[i+3] {
					found = true
				}
			}
			if !found {
				return false
			}
		}
		return true
	case p.Memory:
		return memEq(actual, want)
	case p.Type == "enum" || p.Type == "bool":
		return strings.EqualFold(actual, want)
	}
	return false
}

func memEq(a, b string) bool {
	x, err1 := ParseMemory(a)
	y, err2 := ParseMemory(b)
	return err1 == nil && err2 == nil && x == y
}

func eventSet(s string) string {
	s = strings.ReplaceAll(s, "A", "g$lshzxetd")
	set := map[rune]bool{}
	for _, r := range s {
		set[r] = true
	}
	var out []string
	for r := range set {
		out = append(out, string(r))
	}
	sort.Strings(out)
	return strings.Join(out, "")
}
