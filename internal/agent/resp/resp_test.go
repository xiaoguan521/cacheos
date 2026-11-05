package resp

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
)

func TestRead(t *testing.T) {
	in := "*4\r\n+OK\r\n:42\r\n$5\r\nhe\r\nl\r\n$-1\r\n"
	v, err := Read(bufio.NewReader(strings.NewReader(in)))
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Array) != 4 || v.Array[0].Str != "OK" || v.Array[1].Int != 42 || v.Array[2].Str != "he\r\nl" || !v.Array[3].Null {
		t.Fatalf("%+v", v)
	}
	for _, bad := range []string{"$999999999999\r\n", "*99999999\r\n", "?x\r\n", "+OK\n", "$3\r\nab"} {
		if _, err := Read(bufio.NewReader(strings.NewReader(bad))); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// 参数作为独立 bulk string 发送：含空格、换行的值不会变成额外的命令。
func TestDoEncodesArgsAsBulk(t *testing.T) {
	cli, srv := net.Pipe()
	defer cli.Close()
	got := make(chan Value, 1)
	go func() {
		r := bufio.NewReader(srv)
		v, _ := Read(r)
		got <- v
		srv.Write([]byte("-ERR Unknown option\r\n"))
	}()
	c := New(cli)
	_, err := c.Do(context.Background(), "CONFIG", "SET", "maxmemory", "1gb\r\nFLUSHALL")
	if !IsError(err, "ERR") {
		t.Fatalf("err = %v", err)
	}
	v := <-got
	if s := v.Strings(); len(s) != 4 || s[3] != "1gb\r\nFLUSHALL" {
		t.Fatalf("%q", s)
	}
}

func TestParseInfo(t *testing.T) {
	m := ParseInfo("# Server\r\nsycache_version:9.1.3\r\nrole:master\r\n\r\n# Replication\r\nslave0:ip=10.0.0.2,port=7001,state=online,offset=100,lag=0\r\n")
	if m["sycache_version"] != "9.1.3" || m["role"] != "master" {
		t.Fatal(m)
	}
	if f := ParseFields(m["slave0"]); f["ip"] != "10.0.0.2" || f["lag"] != "0" {
		t.Fatal(f)
	}
}
