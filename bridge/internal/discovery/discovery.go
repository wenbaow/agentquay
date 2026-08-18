// Package discovery 实现 OS 级应用发现（设计文档 §5.8 第三层信息源）：
// Windows 开始菜单快捷方式 + App Paths 注册表、macOS /Applications、Linux .desktop。
// 作用：补充"从未注册过的应用"的安装位置，使 Agent 能按名称拉起任意应用。
//
// 缓存策略：首次查询或启动预扫描后，扫描结果缓存 TTL 秒（默认 5 分钟）；
// 缓存有效期内搜索直接从内存返回（避免反复跑 PowerShell/扫目录），
// 并发搜索走单飞（同一时刻只扫描一次）。
package discovery

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"agentquay/bridge/internal/apps"
)

// 默认缓存有效期。
const defaultCacheTTL = 5 * time.Minute

// Candidate 一个被发现的应用。
type Candidate struct {
	AppID      string             `json:"appId"`
	AppName    string             `json:"appName"`
	Launch     apps.LaunchCommand `json:"launchCommand"`
	Confidence int                `json:"confidence"` // 0-100（匹配得分）
}

// Scanner OS 应用扫描器（带缓存与单飞）。
type Scanner struct {
	enabled bool
	ttl     time.Duration
	logger  *slog.Logger

	mu          sync.Mutex
	cache       []Candidate // 去重后的原始候选（不含查询评分）
	cachedAt    time.Time
	refreshing  bool
	refreshDone chan struct{} // 单飞完成信号
}

// New 创建扫描器。enabled=false 时 Search 返回空。
func New(enabled bool, logger *slog.Logger) *Scanner {
	return &Scanner{enabled: enabled, logger: logger, ttl: defaultCacheTTL}
}

// Enabled 发现开关。
func (s *Scanner) Enabled() bool { return s.enabled }

// SetCacheTTL 设置缓存有效期（≤0 回退默认 5 分钟）。
func (s *Scanner) SetCacheTTL(d time.Duration) {
	if d > 0 {
		s.mu.Lock()
		s.ttl = d
		s.mu.Unlock()
	}
}

// Len 返回缓存中的应用数（未扫描时为 0）。
func (s *Scanner) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.cache)
}

// Refresh 强制扫描一次并写入缓存（bridge 启动后台预扫描、admin 手工刷新用）。
func (s *Scanner) Refresh(ctx context.Context) error {
	scanCtx, cancel := scanTimeout(ctx)
	defer cancel()
	cs := dedup(rankAll(scanPlatform(scanCtx)), 0)
	s.mu.Lock()
	s.cache = cs
	s.cachedAt = time.Now()
	s.mu.Unlock()
	if s.logger != nil {
		s.logger.Debug("OS 应用发现缓存已刷新", "count", len(cs))
	}
	return nil
}

// All 全量返回本机应用（复用缓存，缓存过期时自动刷新），按 AppName 排序。
func (s *Scanner) All(ctx context.Context, limit int) []Candidate {
	if !s.enabled {
		return nil
	}
	cs, _ := s.cached(ctx)
	return dedup(rankAll(cs), limit)
}

// Search 按 query 模糊匹配（名称/标识/可执行文件名），按相关度降序。
// 复用缓存；非空查询时仅保留有匹配的应用（Confidence>0）。
func (s *Scanner) Search(ctx context.Context, query string, limit int) []Candidate {
	if !s.enabled {
		return nil
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return s.All(ctx, limit)
	}
	cs, _ := s.cached(ctx)
	ranked := rankByQuery(cs, q)
	filtered := ranked[:0]
	for _, c := range ranked {
		if c.Confidence > 0 {
			filtered = append(filtered, c)
		}
	}
	return dedup(filtered, limit)
}

// cached 返回去重后的候选集：缓存新鲜直接返回；过期/未扫描时单飞刷新一次。
func (s *Scanner) cached(ctx context.Context) ([]Candidate, error) {
	s.mu.Lock()
	if s.freshLocked() {
		out := append([]Candidate(nil), s.cache...)
		s.mu.Unlock()
		return out, nil
	}
	if s.refreshing {
		done := s.refreshDone
		s.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
		}
		s.mu.Lock()
		out := append([]Candidate(nil), s.cache...)
		s.mu.Unlock()
		return out, nil
	}
	// 本调用负责刷新
	s.refreshing = true
	s.refreshDone = make(chan struct{})
	s.mu.Unlock()

	scanCtx, cancel := scanTimeout(ctx)
	cs := rankAll(scanPlatform(scanCtx))
	cancel()
	cs = dedup(cs, 0)

	s.mu.Lock()
	s.cache = cs
	s.cachedAt = time.Now()
	s.refreshing = false
	close(s.refreshDone)
	s.mu.Unlock()
	return append([]Candidate(nil), cs...), nil
}

// freshLocked 判断缓存是否新鲜（调用方需持有 s.mu）。
func (s *Scanner) freshLocked() bool {
	if s.cachedAt.IsZero() {
		return false
	}
	ttl := s.ttl
	if ttl <= 0 {
		ttl = defaultCacheTTL
	}
	return time.Since(s.cachedAt) < ttl
}

func scanTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 10*time.Second)
}

// rankAll 全量扫描结果按名称排序（保持确定性）。不修改输入切片。
func rankAll(cs []Candidate) []Candidate {
	out := append([]Candidate(nil), cs...)
	for i := range out {
		out[i].Confidence = scoreByName(out[i].AppName, "")
	}
	return out
}

// rankByQuery 按查询打分。不修改输入切片；查询与名称均按小写匹配（大小写不敏感）。
func rankByQuery(cs []Candidate, q string) []Candidate {
	lq := strings.ToLower(q)
	out := append([]Candidate(nil), cs...)
	for i := range out {
		out[i].Confidence = scoreByName(out[i].AppName, lq)
		if out[i].Confidence == 0 {
			if strings.Contains(strings.ToLower(out[i].AppID), lq) {
				out[i].Confidence = 70
			} else if strings.Contains(strings.ToLower(filepath.Base(out[i].Launch.ExecPath)), lq) {
				out[i].Confidence = 60
			}
		}
	}
	return out
}

// scoreByName 名称匹配得分：完全一致 100 / 前缀 90 / 包含 80 / 无匹配 0。
func scoreByName(name, q string) int {
	if q == "" {
		return 50
	}
	lname := strings.ToLower(name)
	lq := strings.ToLower(q)
	if lname == lq {
		return 100
	}
	if strings.HasPrefix(lname, lq) {
		return 90
	}
	if strings.Contains(lname, lq) {
		return 80
	}
	return 0
}

// dedup 按 AppID 去重保留最高分，过滤无效项并按得分降序。
func dedup(cs []Candidate, limit int) []Candidate {
	best := make(map[string]Candidate, len(cs))
	for _, c := range cs {
		if c.AppID == "" || c.Launch.ExecPath == "" {
			continue
		}
		prev, ok := best[c.AppID]
		if !ok || c.Confidence > prev.Confidence {
			best[c.AppID] = c
		}
	}
	out := make([]Candidate, 0, len(best))
	for _, c := range best {
		out = append(out, c)
	}
	// 按置信度降序
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].Confidence < out[j].Confidence; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// AppIDFrom 由应用名/可执行文件名生成合规 appId（[a-z0-9-]{1,48}，全中文名退化为短哈希）。
// 注意：仅用于 OS 发现的临时标识；SDK 注册的 appId 由应用自行决定，不受此影响。
func AppIDFrom(name, execPath string) string {
	base := ""
	if execPath != "" {
		base = strings.ToLower(strings.TrimSuffix(filepath.Base(execPath), filepath.Ext(execPath)))
	}
	seed := base
	if seed == "" {
		seed = strings.ToLower(name)
	}
	id := sanitize(seed)
	if id == "" {
		return "app-" + shortHash(seed)
	}
	return id
}

// sanitize 仅保留 [a-z0-9-]（大写转小写），其余替换为 '-' 并折叠连续 '-'。
func sanitize(s string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r - 'A' + 'a')
			prevDash = false
		case r == '-':
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			prevDash = true
		default:
			if !prevDash && b.Len() > 0 {
				b.WriteByte('-')
			}
			prevDash = true
		}
	}
	id := strings.Trim(b.String(), "-")
	if len(id) > 48 {
		id = id[:48]
	}
	return id
}

func shortHash(seed string) string {
	h := uint64(0)
	for i := 0; i < len(seed); i++ {
		h = h*31 + uint64(seed[i])
	}
	const chars = "0123456789abcdef"
	var b [6]byte
	for i := range b {
		b[5-i] = chars[h&0xf]
		h >>= 4
	}
	return string(b[:])
}
