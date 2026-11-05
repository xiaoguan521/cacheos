// Package secretbox 用控制台本机密钥（AES-256-GCM）加密保存敏感字段，例如证书私钥（规格书 9.3）。
// 密钥文件在首次启动时随机生成，权限 0600，只存在控制台数据目录中，不进数据库、不出现在日志与 API 响应。
package secretbox

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Box 负责加解密。
type Box struct {
	aead cipher.AEAD
	id   string
}

// LoadOrCreate 读取密钥文件，不存在时生成 32 字节随机密钥。
func LoadOrCreate(path string) (*Box, error) {
	key, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		key = make([]byte, 32)
		if _, err := rand.Read(key); err != nil {
			return nil, err
		}
		if err := os.WriteFile(path, key, 0o600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("secretbox: %s must hold 32 bytes", path)
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(blk)
	if err != nil {
		return nil, err
	}
	h := sha256.Sum256(append([]byte("etos-secretbox-id\n"), key...))
	return &Box{aead: aead, id: hex.EncodeToString(h[:8])}, nil
}

// ID 返回密钥的标识（单向哈希的前 8 字节），用于确认多台控制台使用同一把密钥；不能反推出密钥。
func (b *Box) ID() string { return b.id }

// Seal 加密明文，返回 "v1:" + base64(nonce || 密文)。
func (b *Box) Seal(plain string) (string, error) {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	out := b.aead.Seal(nonce, nonce, []byte(plain), nil)
	return "v1:" + base64.StdEncoding.EncodeToString(out), nil
}

// Open 解密 Seal 的结果。
func (b *Box) Open(sealed string) (string, error) {
	raw, ok := strings.CutPrefix(sealed, "v1:")
	if !ok {
		return "", errors.New("secretbox: unknown format")
	}
	data, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", err
	}
	n := b.aead.NonceSize()
	if len(data) < n {
		return "", errors.New("secretbox: short ciphertext")
	}
	plain, err := b.aead.Open(nil, data[:n], data[n:], nil)
	if err != nil {
		return "", errors.New("secretbox: decryption failed")
	}
	return string(plain), nil
}
