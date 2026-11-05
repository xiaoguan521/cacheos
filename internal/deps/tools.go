//go:build tools

// Package deps 固定 MVP 阶段用到的第三方依赖，确保 go mod tidy / vendor 保留它们。
// 依赖许可证均为 MIT / BSD / Apache-2.0（规格书第 3 节）。真正使用后可从此处删除。
package deps

import (
	_ "github.com/gin-gonic/gin"
	_ "github.com/glebarez/sqlite"
	_ "github.com/go-gormigrate/gormigrate/v2"
	_ "github.com/go-ldap/ldap/v3" // LDAP 登录（P9，MIT）
	_ "github.com/golang-jwt/jwt/v5"
	_ "github.com/gorilla/websocket"
	_ "github.com/pquerna/otp/totp"
	_ "golang.org/x/crypto/acme"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/term" // 离线签名工具读取口令时不回显
	_ "google.golang.org/grpc"
	_ "google.golang.org/protobuf/proto"
	_ "gorm.io/driver/postgres"
	_ "gorm.io/gen"
	_ "gorm.io/gorm"
)
