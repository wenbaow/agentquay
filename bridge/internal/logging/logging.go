// Package logging 初始化 slog 日志（终端或 ~/.agentquay/agentquay.log）。
package logging

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// Init 初始化日志器。file 为空时输出到 stderr。
// 返回日志器与需要关闭的文件句柄（stderr 时无需关闭）。
func Init(level, file string) (*slog.Logger, io.Closer, error) {
	var lvl slog.Level
	switch strings.ToLower(level) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}

	var w io.Writer = os.Stderr
	var closer io.Closer = nopCloser{}
	if file != "" {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return nil, nil, err
		}
		f, err := os.OpenFile(file, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, nil, err
		}
		w = f
		closer = f
	}

	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: lvl})
	return slog.New(handler), closer, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
