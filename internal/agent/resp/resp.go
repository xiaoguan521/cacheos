// Package resp 是 sycache-agent 访问本机实例用的最小 RESP2 客户端（CacheOS 规格书 6.2）。
//
// 只经 Unix Socket 连接本机实例，以管理账号认证；不实现 pipeline、pub/sub、事务。
// 命令名由调用方以常量给出，参数一律作为独立的 bulk string 发送，不存在拼接注入。
package resp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Value 是一个 RESP 应答。
type Value struct {
	Kind  byte // '+' '-' ':' '$' '*'
	Str   string
	Int   int64
	Null  bool
	Array []Value
}

// Error 是服务端返回的错误应答。
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

// Prefix 返回错误前缀（如 ERR、NOPERM、LOADING、READONLY）。
func (e *Error) Prefix() string {
	if i := strings.IndexByte(e.Msg, ' '); i > 0 {
		return e.Msg[:i]
	}
	return e.Msg
}

// IsError 判断 err 是否为指定前缀的服务端错误。
func IsError(err error, prefix string) bool {
	var e *Error
	return errors.As(err, &e) && e.Prefix() == prefix
}

// Text 返回字符串值（简单字符串或 bulk string）。
func (v Value) Text() string { return v.Str }

// Strings 把数组应答转为字符串切片。
func (v Value) Strings() []string {
	out := make([]string, len(v.Array))
	for i, e := range v.Array {
		if e.Kind == ':' {
			out[i] = strconv.FormatInt(e.Int, 10)
		} else {
			out[i] = e.Str
		}
	}
	return out
}

// Conn 是一条到实例的连接。不是并发安全的。
type Conn struct {
	c       net.Conn
	r       *bufio.Reader
	Timeout time.Duration
}

// Limits：单个 bulk 与数组的上限，防止异常应答耗尽 agent 内存。
const (
	MaxBulk  = 64 << 20
	MaxArray = 1 << 20
)

// Dial 连接 Unix Socket 并（可选）以用户名口令认证。
func Dial(ctx context.Context, socket, user, pass string) (*Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	cn := New(c)
	if pass != "" {
		if _, err := cn.Do(ctx, "AUTH", user, pass); err != nil {
			c.Close()
			return nil, fmt.Errorf("auth: %w", err)
		}
	}
	return cn, nil
}

// New 包装一条已建立的连接（测试用）。
func New(c net.Conn) *Conn { return &Conn{c: c, r: bufio.NewReader(c), Timeout: 10 * time.Second} }

// Close 关闭连接。
func (c *Conn) Close() error { return c.c.Close() }

// Do 发送一条命令并读取应答；服务端错误以 *Error 返回。
func (c *Conn) Do(ctx context.Context, args ...string) (Value, error) {
	dl := time.Now().Add(c.Timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(dl) {
		dl = d
	}
	_ = c.c.SetDeadline(dl)
	var b strings.Builder
	b.WriteString("*" + strconv.Itoa(len(args)) + "\r\n")
	for _, a := range args {
		b.WriteString("$" + strconv.Itoa(len(a)) + "\r\n" + a + "\r\n")
	}
	if _, err := io.WriteString(c.c, b.String()); err != nil {
		return Value{}, err
	}
	v, err := Read(c.r)
	if err != nil {
		return Value{}, err
	}
	if v.Kind == '-' {
		return v, &Error{Msg: v.Str}
	}
	return v, nil
}

// Read 读取一个 RESP2 值。
func Read(r *bufio.Reader) (Value, error) {
	line, err := readLine(r)
	if err != nil {
		return Value{}, err
	}
	if len(line) == 0 {
		return Value{}, errors.New("resp: empty line")
	}
	v := Value{Kind: line[0]}
	body := line[1:]
	switch v.Kind {
	case '+', '-':
		v.Str = body
	case ':':
		v.Int, err = strconv.ParseInt(body, 10, 64)
	case '$':
		n, perr := strconv.Atoi(body)
		if perr != nil || n > MaxBulk {
			return v, fmt.Errorf("resp: bad bulk length %q", body)
		}
		if n < 0 {
			v.Null = true
			return v, nil
		}
		buf := make([]byte, n+2)
		if _, err = io.ReadFull(r, buf); err != nil {
			return v, err
		}
		v.Str = string(buf[:n])
	case '*':
		n, perr := strconv.Atoi(body)
		if perr != nil || n > MaxArray {
			return v, fmt.Errorf("resp: bad array length %q", body)
		}
		if n < 0 {
			v.Null = true
			return v, nil
		}
		v.Array = make([]Value, 0, n)
		for i := 0; i < n; i++ {
			e, err := Read(r)
			if err != nil {
				return v, err
			}
			v.Array = append(v.Array, e)
		}
	default:
		return v, fmt.Errorf("resp: unexpected type %q", v.Kind)
	}
	return v, err
}

func readLine(r *bufio.Reader) (string, error) {
	s, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	if len(s) < 2 || s[len(s)-2] != '\r' {
		return "", errors.New("resp: malformed line")
	}
	return s[:len(s)-2], nil
}

// ParseInfo 把 INFO 应答解析为 key → value。
func ParseInfo(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || line[0] == '#' {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			m[k] = v
		}
	}
	return m
}

// ParseFields 解析 INFO 中 "a=1,b=2" 形式的值。
func ParseFields(s string) map[string]string {
	m := map[string]string{}
	for _, kv := range strings.Split(s, ",") {
		if k, v, ok := strings.Cut(kv, "="); ok {
			m[k] = v
		}
	}
	return m
}
