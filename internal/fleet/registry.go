// internal/fleet/registry.go:控制面的被控节点名单(node registry)。
//
// 一行一 Node 落 <fleet root>/nodes.jsonl;写入走「读全量 → 覆盖同 node_id →
// 临时文件 + os.Rename 原子替换」,权限 0600(含 cert fingerprint,不给他用户读)。
// node_id 是控制面权威的唯一查询键;agent_id 是 agent 自报,只作参考。
package fleet

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/Daedalusys/daedalus-sdk/dirs"
	"github.com/Daedalusys/daedalus-sdk/objectmodel"
)

const (
	// FleetDirEnv 覆盖 fleet 根目录;值必须绝对路径(相对或空值直接报错)。
	FleetDirEnv = "DAEDALUS_FLEET_DIR"
	// FleetDirSystem 是镜像内 fleet 根目录。
	FleetDirSystem = "/var/lib/daedalus/fleet"
	// FleetDirHomeRel 是 $HOME 兜底相对路径。
	FleetDirHomeRel = ".local/share/daedalus/fleet"

	// nodesFileName 是注册表文件名。
	nodesFileName = "nodes.jsonl"
)

// Node 是控制面对外的一个被控节点形状(node list 直接渲染,不再二次加工)。
type Node struct {
	NodeID           string                   `json:"node_id"`
	AgentID          string                   `json:"agent_id"`
	Kind             objectmodel.MachineKind  `json:"kind"`
	Addr             string                   `json:"addr"`
	Distro           string                   `json:"distro"`
	DistroVersion    string                   `json:"distro_version"`
	Kernel           string                   `json:"kernel"`
	Arch             string                   `json:"arch"`
	Hostname         string                   `json:"hostname"`
	AgentVersion     string                   `json:"agent_version"`
	SnapshotCap      string                   `json:"snapshot_capability"`
	SupportLevel     objectmodel.SupportLevel `json:"support_level"`
	CertFingerprint  string                   `json:"cert_fingerprint"`
	RegisteredAtUnix int64                    `json:"registered_at_unix"`
	LastSeenUnix     int64                    `json:"last_seen_unix"`
}

// Registry 是节点名单的句柄:持有已解析的 fleet 根目录。
type Registry struct {
	root string
}

// OpenRegistry 解析 fleet 根目录(env → 系统探测 → $HOME 兜底),相对路径 fail-closed。
func OpenRegistry() (*Registry, error) {
	root, err := dirs.Dir(FleetDirEnv, FleetDirSystem, FleetDirHomeRel)
	if err != nil {
		return nil, fmt.Errorf("fleet: 解析 fleet 根目录失败: %w", err)
	}
	return &Registry{root: root}, nil
}

// Root 返回已解析的 fleet 根目录(plan 运行记录与注册表同根落盘)。
func (r *Registry) Root() string {
	return r.root
}

// path 返回注册表文件路径。
func (r *Registry) path() string {
	return filepath.Join(r.root, nodesFileName)
}

// Put 写入或覆盖一个节点:同 node_id 后者胜;整表原子替换,不做行级追加。
func (r *Registry) Put(n Node) error {
	if n.NodeID == "" {
		return fmt.Errorf("fleet: Node.NodeID 为空:注册表以 node_id 为唯一键")
	}
	existing, err := r.load()
	if err != nil {
		return err
	}
	replaced := false
	for i := range existing {
		if existing[i].NodeID == n.NodeID {
			existing[i] = n
			replaced = true
			break
		}
	}
	if !replaced {
		existing = append(existing, n)
	}
	return r.store(existing)
}

// Get 查一个节点;未注册返回 ok=false 且 err 为 nil(调用方靠 ok 区分,不靠 err)。
func (r *Registry) Get(nodeID string) (Node, bool, error) {
	all, err := r.load()
	if err != nil {
		return Node{}, false, err
	}
	for _, n := range all {
		if n.NodeID == nodeID {
			return n, true, nil
		}
	}
	return Node{}, false, nil
}

// List 返回全部节点,按 node_id 升序(与写入次序无关);空注册表返回空切片
// 而非 nil(下游按数组消费)。
func (r *Registry) List() ([]Node, error) {
	all, err := r.load()
	if err != nil {
		return nil, err
	}
	slices.SortFunc(all, func(a, b Node) int { return strings.Compare(a.NodeID, b.NodeID) })
	return all, nil
}

// Delete 摘掉一个节点的行(node_id 轮换后旧行必须消失,否则同一台机会在
// registry 里留下两条)。节点不存在时报错——静默成功会掩盖轮换的 bug。
func (r *Registry) Delete(nodeID string) error {
	all, err := r.load()
	if err != nil {
		return err
	}
	out := make([]Node, 0, len(all))
	found := false
	for _, n := range all {
		if n.NodeID == nodeID {
			found = true
			continue
		}
		out = append(out, n)
	}
	if !found {
		return fmt.Errorf("fleet: 节点 %s 不存在,无法删除", nodeID)
	}
	return r.store(out)
}

// MarkSeen 更新节点的 last_seen_unix;节点未注册时报错(不凭空造行)。
func (r *Registry) MarkSeen(nodeID string, unix int64) error {
	all, err := r.load()
	if err != nil {
		return err
	}
	for i := range all {
		if all[i].NodeID == nodeID {
			all[i].LastSeenUnix = unix
			return r.store(all)
		}
	}
	return fmt.Errorf("fleet: 节点 %s 未注册,无法更新 last_seen", nodeID)
}

// load 读出注册表全部节点;文件不存在视为空注册表(合法初始态)。
func (r *Registry) load() ([]Node, error) {
	f, err := os.Open(r.path())
	if err != nil {
		if os.IsNotExist(err) {
			return []Node{}, nil
		}
		return nil, fmt.Errorf("fleet: 读取注册表 %s 失败: %w", r.path(), err)
	}
	defer f.Close()

	out := []Node{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var n Node
		if err := json.Unmarshal([]byte(raw), &n); err != nil {
			return nil, fmt.Errorf("fleet: 注册表 %s 第 %d 行非法: %w", r.path(), line, err)
		}
		out = append(out, n)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("fleet: 读取注册表 %s 失败: %w", r.path(), err)
	}
	return out, nil
}

// store 原子落盘整个注册表:同目录临时文件 → fsync → rename 覆盖。
func (r *Registry) store(nodes []Node) error {
	if err := os.MkdirAll(r.root, 0o755); err != nil {
		return fmt.Errorf("fleet: 建 fleet 根目录 %s 失败: %w", r.root, err)
	}
	tmp, err := os.CreateTemp(r.root, ".nodes-*.tmp")
	if err != nil {
		return fmt.Errorf("fleet: 建注册表临时文件失败: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // rename 成功后此处是 no-op

	w := bufio.NewWriter(tmp)
	for _, n := range nodes {
		b, err := json.Marshal(n)
		if err != nil {
			tmp.Close()
			return fmt.Errorf("fleet: 序列化节点 %s 失败: %w", n.NodeID, err)
		}
		if _, err := w.Write(append(b, '\n')); err != nil {
			tmp.Close()
			return fmt.Errorf("fleet: 写注册表失败: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return fmt.Errorf("fleet: 刷注册表失败: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("fleet: 同步注册表失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("fleet: 关闭注册表临时文件失败: %w", err)
	}
	if err := os.Chmod(tmpName, 0o600); err != nil {
		return fmt.Errorf("fleet: 设置注册表权限失败: %w", err)
	}
	if err := os.Rename(tmpName, r.path()); err != nil {
		return fmt.Errorf("fleet: 原子替换注册表失败: %w", err)
	}
	return nil
}
