package paramcat

// Template 是内置参数模板（规格书 8 节 M5）。maxmemory 不在模板中：由部署单元的内存规格决定。
type Template struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Params      map[string]string `json:"params"`
}

// Builtin 是三套内置模板：缓存型、持久型、高并发型。
var Builtin = []Template{
	{
		Name:        "cache",
		Description: "缓存型：关闭持久化，内存满时按 LRU 淘汰全部键，适合可丢失、可重建的数据",
		Params: map[string]string{
			"maxmemory-policy": "allkeys-lru", "save": "", "appendonly": "no",
			"lazyfree-lazy-eviction": "yes", "lazyfree-lazy-expire": "yes", "lazyfree-lazy-server-del": "yes",
			"commandlog-execution-slower-than": "10000", "commandlog-slow-execution-max-len": "256",
		},
	},
	{
		Name:        "persistent",
		Description: "持久型：AOF 每秒刷盘 + 每小时 RDB，内存满时拒绝写入而不是淘汰数据",
		Params: map[string]string{
			"maxmemory-policy": "noeviction", "save": "3600 1", "appendonly": "yes", "appendfsync": "everysec",
			"aof-use-rdb-preamble": "yes", "auto-aof-rewrite-percentage": "100", "auto-aof-rewrite-min-size": "67108864",
			"stop-writes-on-bgsave-error": "yes", "repl-backlog-size": "67108864",
			"commandlog-execution-slower-than": "10000", "commandlog-slow-execution-max-len": "256",
		},
	},
	{
		Name:        "high-concurrency",
		Description: "高并发型：多 IO 线程、较大连接数与复制缓冲，关闭持久化，适合读多写多的热点缓存",
		Params: map[string]string{
			"maxmemory-policy": "allkeys-lfu", "save": "", "appendonly": "no", "io-threads": "4",
			"maxclients": "50000", "tcp-backlog": "4096", "repl-backlog-size": "134217728",
			"lazyfree-lazy-eviction": "yes", "lazyfree-lazy-expire": "yes", "lazyfree-lazy-server-del": "yes",
			"hz": "20", "commandlog-execution-slower-than": "5000", "commandlog-slow-execution-max-len": "512",
		},
	},
}
