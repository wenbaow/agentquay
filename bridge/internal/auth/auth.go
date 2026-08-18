// Package auth 实现 token 钉扎认证（设计文档 §6.2）：~/.agentquay/auth.json 维护 appId → token 映射。
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// TokenPrefix token 前缀（aq_ + 64 位 hex）。
const TokenPrefix = "aq_"

// ErrAppNotFound 应用未注册过（rotate 时）。
var ErrAppNotFound = errors.New("app 未注册")

// Store token 钉扎表。
type Store struct {
	mu     sync.Mutex
	path   string
	tokens map[string]string
}

// Load 加载（或创建）token 存储。
func Load(path string) (*Store, error) {
	s := &Store{path: path, tokens: make(map[string]string)}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// 首次运行：创建空表（目录不存在时一并创建）
			if werr := os.MkdirAll(filepath.Dir(path), 0o755); werr != nil {
				return nil, werr
			}
			if werr := s.save(); werr != nil {
				return nil, werr
			}
			return s, nil
		}
		return nil, err
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &s.tokens); err != nil {
			return nil, fmt.Errorf("解析 auth.json 失败: %w", err)
		}
	}
	return s, nil
}

// Token 查询 appId 的已钉扎 token。
func (s *Store) Token(appID string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tokens[appID]
	return t, ok
}

// Allocate 为 appId 分配新 token（首次注册与轮换共用）：覆盖旧值并落盘。
func (s *Store) Allocate(appID string) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.tokens[appID] = token
	err = s.saveLocked()
	s.mu.Unlock()
	if err != nil {
		return "", err
	}
	return token, nil
}

// Rotate 轮换 appId 的 token（不存在时报 ErrAppNotFound）。
func (s *Store) Rotate(appID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tokens[appID]; !ok {
		return "", ErrAppNotFound
	}
	token, err := newToken()
	if err != nil {
		return "", err
	}
	s.tokens[appID] = token
	if err := s.saveLocked(); err != nil {
		return "", err
	}
	return token, nil
}

func (s *Store) saveLocked() error {
	data, err := json.MarshalIndent(s.tokens, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.path, data, 0o600)
}

func (s *Store) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// newToken 生成 aq_<64位hex>。
func newToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return TokenPrefix + hex.EncodeToString(buf), nil
}
