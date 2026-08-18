// Package peer 提供对"其它运行中的 Bridge 实例"的探测（embedded 让位逻辑用，§步骤 2）。
//
// 多个 Bridge 实例共享 ~/.agentquay/ 下的端口文件与认证表：
//   - 端口文件由最后启动的实例写入，是"权威端口"的唯一来源；
//   - 通过 /admin/status 的 mode 字段区分实例强弱，实现"service 优先、embedded 让位"。
package peer

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"agentquay/bridge/internal/config"
)

// 探测超时：短超时即可判定存活，避免阻塞生命周期监视器。
const probeTimeout = 300 * time.Millisecond

// statusView 最小化的 /admin/status 解析结构。
// 老版本实例没有 mode 字段 → 缺省按 "service" 处理（让位方向安全）。
type statusView struct {
	Mode string `json:"mode"`
}

// ProbeSuperiorPeer 读取端口文件：若指向"非自身"的存活实例，则查询其 mode。
//
// 返回 (端口, mode, true)；仅当 mode == "service" 时调用方应执行让位迁移。
// selfPort 为 0 或与端口文件一致时返回 false。
func ProbeSuperiorPeer(selfPort int, host string) (int, string, bool) {
	port, err := config.ReadPortFile()
	if err != nil || port <= 0 || port == selfPort {
		return 0, "", false
	}
	if !alive(host, port) {
		return 0, "", false
	}
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/admin/status", host, port))
	if err != nil {
		return 0, "", false
	}
	defer resp.Body.Close()
	var st statusView
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return 0, "", false
	}
	mode := st.Mode
	if mode == "" {
		mode = "service" // 老实例无 mode 字段：按更稳定实例处理
	}
	return port, mode, true
}

// alive TCP 探测某端口是否有响应（复用健康检查逻辑）。
func alive(host string, port int) bool {
	client := &http.Client{Timeout: probeTimeout}
	resp, err := client.Get(fmt.Sprintf("http://%s:%d/admin/health", host, port))
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
