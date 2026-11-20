// internal/fleet/registry.go:控制面的被控节点名单(node registry)。
//
// 一行一 Node 落 <fleet root>/nodes.jsonl;写入走「读全量 → 覆盖同 node_id →
// 临时文件 + os.Rename 原子替换」,权限 0600(含 cert fingerprint,不给他用户读)。
// node_id 是控制面权威的唯一查询键;agent_id 是 agent 自报,只作参考。
//
// 身份与观测分家:nodes.jsonl 只管身份(声明字段 + 注册时间),last_seen 的刷新
// 走 append-only 的 seen.log(见 seen.go),markSeen 因此不再整表重写。读侧把侧车
// 折叠到身份表之上,Get/List 给出的仍是折叠后的最新 last_seen。崩溃语义:身份表
// 原子替换、不丢;不 fsync 的 last_seen 可能丢最近的增量。
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
// 写入前先把观测侧车折叠进身份表,写完清空侧车 —— 折叠过的 last_seen 已经是
// 身份表的一部分,侧车再留着只会让下一次读重复折叠。
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
	if err := r.store(existing); err != nil {
		return err
	}
	return r.resetSeen()
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
	if err := r.store(out); err != nil {
		return err
	}
	return r.resetSeen()
}

// MarkSeen 追加一条 last_seen 观测;节点未注册时报错(不凭空造行)。
//
// 身份表一个字节都不动:观测走 append-only 的侧车(见 seen.go),markSeen 因此
// 是 O(1) 追加而不是整表读改写。崩溃可能丢最近的观测(侧车不 fsync),但节点
// 身份不受影响。
//
// 这里读一遍身份表只为"未注册校验"(纯读、无 fsync):要再压这段 O(n) 成本就得
// 上内存索引 / write-behind,那必须重新回答「崩溃时丢什么」,不在这条路径上顺手做。
func (r *Registry) MarkSeen(nodeID string, unix int64) error {
	// 读身份表只为"未注册校验"——观测本身不落在它里面。
	all, err := r.loadBase()
	if err != nil {
		return err
	}
	if !slices.ContainsFunc(all, func(n Node) bool { return n.NodeID == nodeID }) {
		return fmt.Errorf("fleet: 节点 %s 未注册,无法更新 last_seen", nodeID)
	}
	if err := r.appendSeen(nodeID, unix); err != nil {
		return err
	}
	return r.compactSeenIfNeeded(all)
}

// markSeenKnown 追加一条 last_seen 观测,**不读身份表**。
//
// 契约:调用方必须已持有身份表快照(all),并且**已自行校验过** nodeID 已注册。
// MarkSeen 的"未注册必须报错"是对外的 fail-fast 边界(谁都能调,故自己读表);
// 热路径上的调用方(Scheduler.Run)已经在整批开始前一次 List() 里对每台机器做过
// 未注册校验,再重放整张表纯属重复劳动 —— 一次 Plan 有 2n 次 markSeen,每次
// 重放 n 行身份表,合计是二次曲线(issue #184)。
//
// 观测侧的语义一字不减:追加不 fsync(崩溃只丢最近的 last_seen)、行数越过阈值
// 仍压实成"每个被观测过的节点一行"、压实仍是原子替换。
func (r *Registry) markSeenKnown(nodeID string, unix int64, all []Node) error {
	if err := r.appendSeen(nodeID, unix); err != nil {
		return err
	}
	return r.compactSeenIfNeeded(all)
}

// compactSeenIfNeeded 只在侧车**字节数**够得着压实阈值时才去数行并压实。
//
// 每行至少 1 字节 ⇒ 行数 ≤ 字节数,所以 size < 阈值时必然 lines < 阈值,
// 不可能触发压实。热路径因此只剩一次 Stat,而不是把整个侧车读一遍数行;
// 真要压实时仍走 compactSeen(同一套阈值口径、同一套原子替换)。
func (r *Registry) compactSeenIfNeeded(all []Node) error {
	info, err := os.Stat(r.seenPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("fleet: 读取观测侧车 %s 失败: %w", r.seenPath(), err)
	}
	if info.Size() < int64(r.seenCompactThreshold(len(all))) {
		return nil
	}
	return r.compactSeen(all)
}

// load 读出注册表全部节点(身份表 + 折叠后的观测);文件不存在视为空注册表
// (合法初始态)。
func (r *Registry) load() ([]Node, error) {
	all, err := r.loadBase()
	if err != nil {
		return nil, err
	}
	return r.foldSeen(all)
}

// loadBase 只读身份表(不折叠观测):写路径用它读出待改的行,读路径经 load 折叠。
func (r *Registry) loadBase() ([]Node, error) {
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
