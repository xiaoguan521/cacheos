module cacheos/test/clients/gomod

go 1.21

require github.com/redis/go-redis/v9 v9.7.3

// replace 指令由 client_matrix_test.py 在临时副本中按 clients.lock 的缓存目录追加，离线构建、不访问模块代理。
