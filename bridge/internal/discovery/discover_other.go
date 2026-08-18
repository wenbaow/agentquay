//go:build !windows && !darwin && !linux

package discovery

import "context"

// scanPlatform 其他平台暂无发现实现，返回空。
func scanPlatform(ctx context.Context) []Candidate { return nil }
