// internal/fleet/liveness.go:下发工作之前的**活性预检**。
//
// 心跳口径(策略 [fleet].heartbeat_timeout_seconds)只回答"这台机还活着吗":
// 它的时限只作用于一次 Heartbeat 往返,**绝不**套到 ApplyIntent 上 —— 装一个
// 内核包合法地能跑几分钟,拿心跳口径判工作 RPC 会把真实装包全判死
// (工作 RPC 的兜底仍是 defaultCallTimeout)。
//
// 预检针对的是"连得上但不回"这一类故障(进程卡死 / GC 停顿 / 磁盘 hang /
// 网络黑洞);连接被拒本来就立刻返回,不必靠心跳抓。
package fleet

import (
	"context"
	"fmt"
	"time"

	agentv1 "github.com/Daedalusys/daedalus-sdk/proto/agent/v1"
)

// heartbeatTimeout 返回本次预检的时限。策略校验已强制该键为正,这里的回落只防
// 直接构造 &Scheduler{...} 时的零值(WithTimeout(0) 会让预检瞬间过期)。
func (s *Scheduler) heartbeatTimeout() time.Duration {
	if s.HeartbeatTimeout > 0 {
		return s.HeartbeatTimeout
	}
	return defaultCallTimeout
}

// dialHeartbeat 是生产预检实现:在心跳时限内建连并调一次 Heartbeat。
//
// 判据:rpc 成功**且** Ack=true 才算活。Ack=false 是 agent 自己说"我现在不该
// 接活"(例如正在退出),按失败处理 —— 只看 err==nil 会把这种节点当成活的。
//
// 预检结果只对本次调用有效:不写 registry 的 last_seen —— 那是 markSeen 的
// 职责,让一次探测改写观测事实会把"agent 自己报活"与"控制面探测成功"混同。
func (s *Scheduler) dialHeartbeat(ctx context.Context, t DispatchTarget) error {
	timeout := s.heartbeatTimeout()

	cfg := s.dial
	cfg.Addr = t.Node.Addr
	cfg.CallTimeout = timeout

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	client, closer, err := Dial(callCtx, cfg)
	if err != nil {
		return fmt.Errorf("节点 %s 建连失败(心跳时限 %s): %w", t.Node.NodeID, timeout, err)
	}
	defer func() { _ = closer() }()

	resp, err := client.Heartbeat(callCtx, &agentv1.HeartbeatRequest{NodeId: t.Node.NodeID})
	if err != nil {
		return fmt.Errorf("节点 %s 未在 %s 内回心跳: %w", t.Node.NodeID, timeout, err)
	}
	if !resp.GetAck() {
		return fmt.Errorf("节点 %s 回 ack=false(心跳时限 %s):agent 自认不该接活", t.Node.NodeID, timeout)
	}
	return nil
}
