// go-redis 客户端对 sycache 的兼容性用例。由 test/clients/client_matrix_test.py 提供拓扑端口与证书。
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type topology struct {
	Standalone      int               `json:"standalone"`
	TLS             int               `json:"tls"`
	Cluster         []int             `json:"cluster"`
	Sentinels       []int             `json:"sentinels"`
	MasterName      string            `json:"master_name"`
	SentinelPrimary int               `json:"sentinel_primary"`
	Certs           map[string]string `json:"certs"`
}

var ctx = context.Background()

func check(ok bool, msg string) {
	if !ok {
		fmt.Println("FAIL:", msg)
		os.Exit(1)
	}
	fmt.Println("ok  ", msg)
}

func must[T any](v T, err error) T {
	if err != nil {
		fmt.Println("FAIL:", err)
		os.Exit(1)
	}
	return v
}

func addr(p int) string { return "127.0.0.1:" + strconv.Itoa(p) }

func main() {
	var t topology
	if err := json.Unmarshal([]byte(os.Getenv("TOPOLOGY")), &t); err != nil {
		panic(err)
	}
	fmt.Println("go-redis", redis.Version())

	// 单机：go-redis v9 默认用 HELLO 3（RESP3），同时验证显式 RESP2
	for _, proto := range []int{3, 2} {
		c := redis.NewClient(&redis.Options{Addr: addr(t.Standalone), Protocol: proto})
		key := fmt.Sprintf("go:k%d", proto)
		must(c.Set(ctx, key, "v", time.Minute).Result())
		check(must(c.Get(ctx, key).Result()) == "v", fmt.Sprintf("standalone RESP%d set/get", proto))
		must(c.HSet(ctx, "go:h", "a", "1", "b", "2").Result())
		check(len(must(c.HGetAll(ctx, "go:h").Result())) == 2, fmt.Sprintf("standalone RESP%d hash", proto))
		cmds := must(c.TxPipelined(ctx, func(p redis.Pipeliner) error {
			p.Incr(ctx, "go:n")
			p.Incr(ctx, "go:n")
			return nil
		}))
		check(len(cmds) == 2, fmt.Sprintf("standalone RESP%d MULTI pipeline", proto))
		s := redis.NewScript("return redis.call('GET', KEYS[1])")
		check(must(s.Run(ctx, c, []string{key}).Text()) == "v", fmt.Sprintf("standalone RESP%d EVALSHA", proto))
		c.Close()
	}

	// 原生集群
	cc := redis.NewClusterClient(&redis.ClusterOptions{Addrs: []string{addr(t.Cluster[0]), addr(t.Cluster[1])}})
	for i := 0; i < 200; i++ {
		must(cc.Set(ctx, fmt.Sprintf("go:c:%d", i), i, 0).Result())
	}
	okAll := true
	for i := 0; i < 200; i++ {
		okAll = okAll && must(cc.Get(ctx, fmt.Sprintf("go:c:%d", i)).Result()) == strconv.Itoa(i)
	}
	check(okAll, "cluster 200 keys across slots")
	cc.Close()

	// 哨兵
	sa := []string{}
	for _, p := range t.Sentinels {
		sa = append(sa, addr(p))
	}
	fc := redis.NewFailoverClient(&redis.FailoverOptions{MasterName: t.MasterName, SentinelAddrs: sa})
	must(fc.Set(ctx, "go:s", "1", 0).Result())
	check(must(fc.Get(ctx, "go:s").Result()) == "1", "sentinel failover client write/read")
	fc.Close()

	// TLS 双向认证
	ca := x509.NewCertPool()
	ca.AppendCertsFromPEM(must(os.ReadFile(t.Certs["ca.crt"])))
	cert := must(tls.LoadX509KeyPair(t.Certs["client.crt"], t.Certs["client.key"]))
	tc := redis.NewClient(&redis.Options{Addr: addr(t.TLS), TLSConfig: &tls.Config{
		RootCAs: ca, Certificates: []tls.Certificate{cert}, ServerName: "localhost", MinVersion: tls.VersionTLS12}})
	must(tc.Set(ctx, "go:tls", "1", 0).Result())
	check(must(tc.Get(ctx, "go:tls").Result()) == "1", "TLS mutual auth set/get")
	tc.Close()
}
