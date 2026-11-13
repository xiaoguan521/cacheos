// go-redis 对扩展数据类型模块（JSON / 布隆过滤器 / 搜索与向量，ADR 0002）的兼容性用例。
// 由 test/clients/module_clients_test.py 提供拓扑端口；MODULE 指定本轮验证的模块。
// 每个用例独立运行：失败只记录不中断，最后统一汇总。
package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

type topology struct {
	Standalone int   `json:"standalone"`
	Cluster    []int `json:"cluster"`
}

var (
	ctx      = context.Background()
	failures []string
)

func addr(p int) string { return "127.0.0.1:" + strconv.Itoa(p) }

func run(name string, fn func() error) {
	var err error
	func() {
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("panic: %v", r)
			}
		}()
		err = fn()
	}()
	if err != nil {
		failures = append(failures, name)
		fmt.Printf("FAIL %s: %v\n", name, err)
		return
	}
	fmt.Println("ok  ", name)
}

// known 登记已知与 Redis 官方模块的差异：期望按已知方式失败；意外通过时报错，提醒更新 modules-known-diff.txt 与用例。
func known(name, reason string, fn func() error) {
	if err := fn(); err != nil {
		fmt.Printf("known-diff %s: %s (%v)\n", name, reason, err)
		return
	}
	failures = append(failures, name)
	fmt.Printf("FAIL %s: 已知差异意外消失（%s），请更新 test/clients/modules-known-diff.txt 与此用例\n", name, reason)
}

func eq(what string, got, want any) error {
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("%s: got %#v, want %#v", what, got, want)
	}
	return nil
}

func first(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

func vec(xs ...float32) string {
	b := make([]byte, 4*len(xs))
	for i, x := range xs {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(x))
	}
	return string(b)
}

func jsonCases(t topology) {
	for _, proto := range []int{3, 2} {
		proto := proto
		run(fmt.Sprintf("json RESP%d set/get/arrappend/numincrby/type/del", proto), func() error {
			c := redis.NewClient(&redis.Options{Addr: addr(t.Standalone), Protocol: proto})
			defer c.Close()
			k := fmt.Sprintf("go:json:%d", proto)
			if _, err := c.JSONSet(ctx, k, "$", `{"n":1,"tags":["a","b"]}`).Result(); err != nil {
				return err
			}
			got, err := c.JSONGet(ctx, k, "$.tags[1]").Result()
			if err != nil {
				return err
			}
			if e := eq("get path", got, `["b"]`); e != nil {
				return e
			}
			app, err := c.JSONArrAppend(ctx, k, "$.tags", `"c"`).Result()
			if err != nil {
				return err
			}
			inc, err := c.JSONNumIncrBy(ctx, k, "$.n", 2).Result()
			if err != nil {
				return err
			}
			typ, err := c.JSONType(ctx, k, "$.tags").Result()
			if err != nil {
				return err
			}
			del, err := c.JSONDel(ctx, k, "$.n").Result()
			if err != nil {
				return err
			}
			return first(eq("arrappend", app, []int64{3}), eq("numincrby", inc, "[3]"), eq("type", typ, []interface{}{"array"}), eq("del", del, int64(1)))
		})
	}
	run("json cluster client routes by slot", func() error {
		addrs := make([]string, len(t.Cluster))
		for i, p := range t.Cluster {
			addrs[i] = addr(p)
		}
		c := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs})
		defer c.Close()
		for i := 0; i < 60; i++ {
			if _, err := c.JSONSet(ctx, fmt.Sprintf("go:jc:%d", i), "$", fmt.Sprintf(`{"i":%d}`, i)).Result(); err != nil {
				return err
			}
		}
		for i := 0; i < 60; i++ {
			got, err := c.JSONGet(ctx, fmt.Sprintf("go:jc:%d", i), "$.i").Result()
			if err != nil {
				return err
			}
			if e := eq("get", got, fmt.Sprintf("[%d]", i)); e != nil {
				return e
			}
		}
		return nil
	})
}

func bloomCases(t topology) {
	for _, proto := range []int{3, 2} {
		proto := proto
		run(fmt.Sprintf("bloom RESP%d reserve/add/exists/madd/mexists/card/info", proto), func() error {
			c := redis.NewClient(&redis.Options{Addr: addr(t.Standalone), Protocol: proto})
			defer c.Close()
			k := fmt.Sprintf("go:bf:%d", proto)
			if _, err := c.BFReserve(ctx, k, 0.01, 1000).Result(); err != nil {
				return err
			}
			a1, err := c.BFAdd(ctx, k, "a").Result()
			if err != nil {
				return err
			}
			a2, err := c.BFAdd(ctx, k, "a").Result()
			if err != nil {
				return err
			}
			ex, err := c.BFExists(ctx, k, "a").Result()
			if err != nil {
				return err
			}
			no, err := c.BFExists(ctx, k, "zzz").Result()
			if err != nil {
				return err
			}
			ma, err := c.BFMAdd(ctx, k, "b", "c").Result()
			if err != nil {
				return err
			}
			me, err := c.BFMExists(ctx, k, "a", "b", "nope").Result()
			if err != nil {
				return err
			}
			card, err := c.BFCard(ctx, k).Result()
			if err != nil {
				return err
			}
			return first(eq("add new", a1, true), eq("add existing", a2, false), eq("exists", ex, true), eq("not exists", no, false),
				eq("madd", ma, []bool{true, true}), eq("mexists", me, []bool{true, true, false}), eq("card", card, int64(3)))
		})
		// valkey-bloom 的 BF.INFO 与 RedisBloom 的回复形态不同：完整形式多返回 "Error rate" 等键，
		// 单字段形式（如 CAPACITY）只回复一个整数而不是 [名称, 值]；go-redis 对两种形态都解析失败。
		// 读取过滤器信息请用 redis-py 或直接发 BF.INFO；BF.EXISTS / ADD / MADD / MEXISTS / CARD 不受影响。
		known(fmt.Sprintf("bloom RESP%d full BF.INFO parse", proto), "go-redis rejects the extra \"Error rate\" key", func() error {
			c := redis.NewClient(&redis.Options{Addr: addr(t.Standalone), Protocol: proto})
			defer c.Close()
			_, err := c.BFInfo(ctx, fmt.Sprintf("go:bf:%d", proto)).Result()
			return err
		})
		known(fmt.Sprintf("bloom RESP%d single-field BF.INFO parse", proto), "go-redis expects [name, value], server returns a bare integer", func() error {
			c := redis.NewClient(&redis.Options{Addr: addr(t.Standalone), Protocol: proto})
			defer c.Close()
			_, err := c.BFInfoCapacity(ctx, fmt.Sprintf("go:bf:%d", proto)).Result()
			return err
		})
	}
	run("bloom cluster client routes by slot", func() error {
		addrs := make([]string, len(t.Cluster))
		for i, p := range t.Cluster {
			addrs[i] = addr(p)
		}
		c := redis.NewClusterClient(&redis.ClusterOptions{Addrs: addrs})
		defer c.Close()
		for i := 0; i < 40; i++ {
			k := fmt.Sprintf("go:bfc:%d", i)
			if _, err := c.BFReserve(ctx, k, 0.01, 100).Result(); err != nil {
				return err
			}
			if _, err := c.BFAdd(ctx, k, "v").Result(); err != nil {
				return err
			}
			ok, err := c.BFExists(ctx, k, "v").Result()
			if err != nil || !ok {
				return fmt.Errorf("exists %s: %v %v", k, ok, err)
			}
		}
		return nil
	})
}

func searchCases(t topology) {
	for _, proto := range []int{2, 3} {
		proto := proto
		name := fmt.Sprintf("search RESP%d create index, KNN, hybrid filter, drop", proto)
		body := func() error {
			c := redis.NewClient(&redis.Options{Addr: addr(t.Standalone), Protocol: proto, UnstableResp3: true})
			defer c.Close()
			idx, pre := fmt.Sprintf("goidx%d", proto), fmt.Sprintf("godoc%d:", proto)
			_, err := c.FTCreate(ctx, idx, &redis.FTCreateOptions{OnHash: true, Prefix: []interface{}{pre}},
				&redis.FieldSchema{FieldName: "v", FieldType: redis.SearchFieldTypeVector, VectorArgs: &redis.FTVectorArgs{
					HNSWOptions: &redis.FTHNSWOptions{Type: "FLOAT32", Dim: 4, DistanceMetric: "L2"}}},
				&redis.FieldSchema{FieldName: "cat", FieldType: redis.SearchFieldTypeTag}).Result()
			if err != nil {
				return err
			}
			for i, d := range []struct {
				v   string
				cat string
			}{{vec(1, 0, 0, 0), "A"}, {vec(0, 1, 0, 0), "B"}, {vec(0, 0, 1, 0), "A"}} {
				if err := c.HSet(ctx, fmt.Sprintf("%s%d", pre, i+1), "v", d.v, "cat", d.cat).Err(); err != nil {
					return err
				}
			}
			opts := &redis.FTSearchOptions{Params: map[string]interface{}{"q": vec(0.9, 0.1, 0, 0)}, DialectVersion: 2,
				Return: []redis.FTSearchReturn{{FieldName: "score"}}}
			ids := func(query string) ([]string, error) {
				var out []string
				for i := 0; i < 40; i++ {
					res, err := c.FTSearchWithArgs(ctx, idx, query, opts).Result()
					if err != nil {
						return nil, err
					}
					out = out[:0]
					for _, d := range res.Docs {
						out = append(out, d.ID)
					}
					if len(out) >= 2 {
						break
					}
					time.Sleep(500 * time.Millisecond) // 回填是异步的
				}
				return out, nil
			}
			knn, err := ids("*=>[KNN 2 @v $q AS score]")
			if err != nil {
				return err
			}
			hyb, err := ids("(@cat:{A})=>[KNN 2 @v $q AS score]")
			if err != nil {
				return err
			}
			if err := c.FTDropIndex(ctx, idx).Err(); err != nil {
				return err
			}
			return first(eq("KNN order", knn, []string{pre + "1", pre + "2"}), eq("hybrid TAG filter", hyb, []string{pre + "1", pre + "3"}))
		}
		if proto == 3 {
			// go-redis 在 RESP3 下期望 FT.SEARCH 回复映射（results / extra_attributes），valkey-search 回复数组，
			// 解析不报错但得不到任何文档；用 go-redis 访问 search 请使用 Protocol: 2。
			known(name, "go-redis RESP3 cannot parse valkey-search FT.SEARCH replies (no documents returned)", body)
		} else {
			run(name, body)
		}
	}
}

func main() {
	var t topology
	if err := json.Unmarshal([]byte(os.Getenv("TOPOLOGY")), &t); err != nil {
		panic(err)
	}
	module := os.Getenv("MODULE")
	fmt.Println("go-redis", redis.Version(), "module="+module)
	switch module {
	case "json":
		jsonCases(t)
	case "bloom":
		bloomCases(t)
	case "search":
		searchCases(t)
	default:
		fmt.Println("FAIL unknown MODULE", module)
		os.Exit(2)
	}
	fmt.Printf("summary: %d failed\n", len(failures))
	if len(failures) > 0 {
		os.Exit(1)
	}
}
