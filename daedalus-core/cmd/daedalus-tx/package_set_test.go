package main

// package_set_test.go —— package.set 适配器的纯函数边界测试(plan daedalus-pkg-kind todo 12)。
//
// 覆盖三层: 边界解析(parsePackageSetArgs 的未知键/尾随数据/缺字段门)、
// 名称与 desired_state 守卫(经 parseAndGuard 管线 helper 测, 逐字镜像 Propose
// 的守卫次序: 解析 → 名称门 → 三元组门 —— 生产里 parsePackageSetArgs 本身不
// 调后两道门, 该形态差已记入 learnings)、适配器注册表(package.set 注册 +
// service.set 零回归)。
//
// 全程零进程调用: 不碰 dnf/rpm, 不覆写任何 mock var(那是 todo 13/14/15 的
// Propose/Apply/Rollback 行为测试区, 分区注释见文件尾部)。

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	// todo 14 编译必需(tx.Step/tx.OpResult 断言类型)。Go 规范要求全部 import 位于
	// 其他声明之前, 无法落在分区内 —— 沿 task-13 先例(import 块补 context/errors)
	// 在头部块追加, 属唯一非 append 改动, 不动任何既有用例。
	// todo 15 再补 bytes/os/path/filepath 与 audit: Rollback 区经真文件往返 +
	// E2E 直接驱动四个子命令编排函数并用真实 JSONL 回放审计链(编译必需)。
	"github.com/Daedalusys/daedalus-sdk/audit"
	"github.com/Daedalusys/daedalus-core/internal/tx"
)

// parseAndGuard 组合管线: parsePackageSetArgs → sanitizePackageName → desiredStateOK。
// 守卫次序与 packageSetAdapter.Propose 逐字一致(package_set.go), 让名称门/三元组门
// 的拒绝形态在纯函数层即可断言, 无需触达 rpm 查询。
func parseAndGuard(raw string) error {
	in, err := parsePackageSetArgs(json.RawMessage(raw))
	if err != nil {
		return err
	}
	if err := sanitizePackageName(in.Name); err != nil {
		return err
	}
	if !desiredStateOK(in.DesiredState) {
		return fmt.Errorf(`unknown desired_state %q (v1 支持 present|absent|latest)`, in.DesiredState)
	}
	return nil
}

// TestParsePackageSetArgs_Happy 钉三元组合法值 + 合法名称的解析结果逐字
// (含白名单字符类允许的 `.` 中段形态, 证明 post-checks 不误伤合法包名)。
func TestParsePackageSetArgs_Happy(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want packageSetArgs
	}{
		{"present", `{"name":"htop","desired_state":"present"}`, packageSetArgs{Name: "htop", DesiredState: "present"}},
		{"absent", `{"name":"htop","desired_state":"absent"}`, packageSetArgs{Name: "htop", DesiredState: "absent"}},
		{"latest", `{"name":"htop","desired_state":"latest"}`, packageSetArgs{Name: "htop", DesiredState: "latest"}},
		// 点号在字符类内: 中段单点是合法包名形态(如带版本流的后缀)。
		{"dotted_name", `{"name":"python3.11","desired_state":"present"}`, packageSetArgs{Name: "python3.11", DesiredState: "present"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parsePackageSetArgs(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("合法 args 意外被拒: %v (raw=%s)", err, tc.raw)
			}
			if got != tc.want {
				t.Errorf("解析结果 = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestParsePackageSetArgs_RejectsUnknownFields 钉 DisallowUnknownFields 守门:
// schema 之外的多余键即拒(防手改日志注入附加字段)。
func TestParsePackageSetArgs_RejectsUnknownFields(t *testing.T) {
	_, err := parsePackageSetArgs(json.RawMessage(`{"name":"htop","desired_state":"present","extra":"x"}`))
	if err == nil {
		t.Fatal("含未知键 extra 的 args 必须被拒")
	}
	// 前缀逐字钉; stdlib 的 unknown field 消息属实现细节, 只用 Contains 钉语义。
	if !strings.HasPrefix(err.Error(), "invalid package.set args: ") {
		t.Errorf("错误串前缀 = %q, want 以 %q 开头", err.Error(), "invalid package.set args: ")
	}
	if !strings.Contains(err.Error(), `unknown field "extra"`) {
		t.Errorf("错误串应点名未知字段 extra(Contains 理由: stdlib 消息格式非本项目契约): %q", err.Error())
	}
}

// TestParsePackageSetArgs_RejectsTrailingData 钉 dec.More() 守门: 两个 JSON
// 文档拼接必须整体拒绝, 绝不静默取第一个。
func TestParsePackageSetArgs_RejectsTrailingData(t *testing.T) {
	raw := `{"name":"htop","desired_state":"present"}{"name":"nginx","desired_state":"absent"}`
	_, err := parsePackageSetArgs(json.RawMessage(raw))
	if err == nil {
		t.Fatal("尾随第二个 JSON 文档必须被拒")
	}
	// 该错误串由本项目字面构造(无 %w 包装), 逐字全等断言。
	if want := "invalid package.set args: JSON 文档后有尾随数据"; err.Error() != want {
		t.Errorf("错误串 = %q, want %q", err.Error(), want)
	}
}

// TestParsePackageSetArgs_RejectsMissingDesiredState 钉缺键与空串两形态都在
// 解析层拒绝(空串与缺席在线上等价为同一形态)。
func TestParsePackageSetArgs_RejectsMissingDesiredState(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"missing_key", `{"name":"htop"}`},
		{"empty_string", `{"name":"htop","desired_state":""}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePackageSetArgs(json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("缺 desired_state(%s) 必须被拒: raw=%s", tc.name, tc.raw)
			}
			// 该错误串由本项目字面构造, 逐字全等断言。
			if want := `invalid package.set args: 缺 "desired_state" 字段`; err.Error() != want {
				t.Errorf("错误串 = %q, want %q", err.Error(), want)
			}
		})
	}
}

// TestParsePackageSetArgs_RejectsBadName 钉名称门的全部拒绝出口(经 parseAndGuard
// 管线, 守卫次序同 Propose)。每个 sub-test 隔离一道防线:
//   - 字符集门: `;`/空格/`@`/空字节/空名/路径分隔符落在白名单字符类之外;
//   - 前缀门: 单独 `.` 或 `.hidden` 只被 HasPrefix(".") 拒;
//   - 双点门: `foo..bar` 是唯一只被 Contains("..") 拒的形态(task-6 实测教训:
//     裸 `..` 被前缀门双覆盖, 摘掉双点检查后仍挂前缀门就测不出回归);
//   - glob 门(review C-1): 通配星号落在白名单字符类内, 只被 set 域专属的
//     Contains 星号检查拒——`*` 与 `bash*` 钉死批量变更放大面。
func TestParsePackageSetArgs_RejectsBadName(t *testing.T) {
	cases := []struct {
		name string
		bad  string
	}{
		{"semicolon_and_space", "htop; rm -rf /"},
		{"bare_dotdot", ".."},
		{"hidden_prefix", ".hidden"},
		{"separator", "foo/bar"},
		{"backslash", `foo\bar`},
		{"at_sign", "foo@bar"},
		{"null_byte", "foo\x00bar"},
		{"empty", ""},
		{"isolating_dotdot", "foo..bar"},
		{"glob_star", "*"},
		{"glob_suffix", "bash*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(packageSetArgs{Name: tc.bad, DesiredState: "present"})
			if err != nil {
				t.Fatalf("构造测试输入失败: %v", err)
			}
			err = parseAndGuard(string(raw))
			if err == nil {
				t.Fatalf("非法包名 %q 必须被拒", tc.bad)
			}
			// 名称门错误串由本项目字面构造, 逐字全等断言。
			if want := fmt.Sprintf("Invalid package name or pattern: %s", tc.bad); err.Error() != want {
				t.Errorf("错误串 = %q, want %q", err.Error(), want)
			}
		})
	}
}

// TestParsePackageSetArgs_RejectsBadDesiredState 钉三元组门: `"weird"` 能通过
// 解析(非空)但在 desiredStateOK 处被拒, 错误串逐字含 v1 支持集合说明。
func TestParsePackageSetArgs_RejectsBadDesiredState(t *testing.T) {
	err := parseAndGuard(`{"name":"htop","desired_state":"weird"}`)
	if err == nil {
		t.Fatal(`表外 desired_state "weird" 必须被拒`)
	}
	// 该错误串由本项目字面构造(fmt 模板逐字对照 Propose 守卫), 全等断言。
	if want := `unknown desired_state "weird" (v1 支持 present|absent|latest)`; err.Error() != want {
		t.Errorf("错误串 = %q, want %q", err.Error(), want)
	}
}

// TestPackageSet_DesiredStateVerbMap 钉 packageSetVerbs 冻结映射: 恰 3 键、
// present→install/absent→remove/latest→upgrade 逐字, 防多塞键或改值。
func TestPackageSet_DesiredStateVerbMap(t *testing.T) {
	want := map[string]string{
		"present": "install",
		"absent":  "remove",
		"latest":  "upgrade",
	}
	if len(packageSetVerbs) != len(want) {
		t.Fatalf("packageSetVerbs 键数 = %d, want %d (表: %v)", len(packageSetVerbs), len(want), packageSetVerbs)
	}
	for state, verb := range want {
		t.Run(state, func(t *testing.T) {
			got, ok := packageSetVerbs[state]
			if !ok {
				t.Fatalf("缺键 %q", state)
			}
			if got != verb {
				t.Errorf("packageSetVerbs[%q] = %q, want %q", state, got, verb)
			}
		})
	}
	// 反向扫: 任何多余键(不在冻结表内)都属漂移。
	for state := range packageSetVerbs {
		if _, ok := want[state]; !ok {
			t.Errorf("packageSetVerbs 多出表外键 %q", state)
		}
	}
}

// TestAdapterRegistry_PackageSet 钉 todo 11 的注册: package.set 在 registry 且
// 类型正确; 顺带钉 service.set 注册零回归。
func TestAdapterRegistry_PackageSet(t *testing.T) {
	a, ok := lookupAdapter("package.set")
	if !ok {
		t.Fatal(`lookupAdapter("package.set") 未命中 —— 注册缺失`)
	}
	if _, isPkg := a.(packageSetAdapter); !isPkg {
		t.Errorf(`package.set 适配器类型 = %T, want packageSetAdapter`, a)
	}

	s, ok := lookupAdapter("service.set")
	if !ok {
		t.Fatal(`lookupAdapter("service.set") 未命中 —— package.set 注册引入回归`)
	}
	if _, isSvc := s.(serviceSetAdapter); !isSvc {
		t.Errorf(`service.set 适配器类型 = %T, want serviceSetAdapter`, s)
	}
}

// ─────────────────── todo 13/14/15 追加区(勿改上方 8 函数) ───────────────────
// 后续 worker 在本分隔线之后 append Propose/Apply/Rollback 行为测试:
// mock 注入契约见 package_set.go 头部注释(六个包级 var 缝 + geteuid + withTxID 通道)。

// stubRpmQuery 覆写 rpmQueryFn 注入缝并登记 t.Cleanup 还原 —— plan 325 行的
// "defer 中恢复"与本形态语义等价(测试函数返回时执行), 沿用 todo 9/12 既成惯例。
// 生产调用点自 todo 7 起就走 var 缝, 本 helper 即 oracle C3 mock 契约的消费端。
func stubRpmQuery(t *testing.T, fn func(context.Context, string) (bool, string, error)) {
	t.Helper()
	orig := rpmQueryFn
	t.Cleanup(func() { rpmQueryFn = orig })
	rpmQueryFn = fn
}

// TestPackageSet_Propose_HappyInstalled 钉已装现状的双快照: before 逐字段回显
// stub 的 (installed=true, version), after 按 desired_state 映射 verb, 且 stub
// 收到的 name 实参是解析后的值(钉查询目标是白名单校验过的包名)。
func TestPackageSet_Propose_HappyInstalled(t *testing.T) {
	var calls int
	var gotName string
	stubRpmQuery(t, func(_ context.Context, name string) (bool, string, error) {
		calls++
		gotName = name
		return true, "2.4.1-1.fc40", nil
	})

	before, after, err := packageSetAdapter{}.Propose(context.Background(),
		json.RawMessage(`{"name":"htop","desired_state":"present"}`))
	if err != nil {
		t.Fatalf("合法 args + 已装现状不应失败: %v", err)
	}
	if calls != 1 {
		t.Errorf("rpm 查询调用次数 = %d, want 1", calls)
	}
	if gotName != "htop" {
		t.Errorf("传给 rpm 查询的 name = %q, want %q", gotName, "htop")
	}

	var b packageSetBefore
	if err := json.Unmarshal(before, &b); err != nil {
		t.Fatalf("before_state 非法 JSON(%v): %s", err, before)
	}
	if b != (packageSetBefore{Name: "htop", Installed: true, Version: "2.4.1-1.fc40"}) {
		t.Errorf("before = %+v, want {htop true 2.4.1-1.fc40}", b)
	}
	var a packageSetAfter
	if err := json.Unmarshal(after, &a); err != nil {
		t.Fatalf("after_state 非法 JSON(%v): %s", err, after)
	}
	if a != (packageSetAfter{Name: "htop", DesiredState: "present", Verb: "install"}) {
		t.Errorf("after = %+v, want {htop present install}", a)
	}
}

// TestPackageSet_Propose_HappyNotInstalled 钉未装形态: before 里 "version" 键
// 整体缺席(omitempty 实态, 用 map 解 JSON 断键而非 struct 零值——后者分不清
// 缺席与空串)且 installed=false 在位; after 的 verb 由 desired_state(absent→
// remove)独立决定, 证明两份快照彼此独立于 stub 布尔。
func TestPackageSet_Propose_HappyNotInstalled(t *testing.T) {
	stubRpmQuery(t, func(context.Context, string) (bool, string, error) {
		return false, "", nil
	})

	before, after, err := packageSetAdapter{}.Propose(context.Background(),
		json.RawMessage(`{"name":"htop","desired_state":"absent"}`))
	if err != nil {
		t.Fatalf("合法 args + 未装现状不应失败: %v", err)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(before, &m); err != nil {
		t.Fatalf("before_state 非法 JSON(%v): %s", err, before)
	}
	if _, has := m["version"]; has {
		t.Errorf(`未装时 before 不应含 "version" 键(omitempty 缺席), 实际: %s`, before)
	}
	if _, has := m["installed"]; !has {
		t.Fatalf(`before 缺 "installed" 键(恒在), 实际: %s`, before)
	}
	var installed bool
	if err := json.Unmarshal(m["installed"], &installed); err != nil {
		t.Fatalf("installed 键非布尔(%v): %s", err, m["installed"])
	}
	if installed {
		t.Errorf("installed = true, want false(stub 返回未装)")
	}
	if want := `"name":"htop"`; !strings.Contains(string(before), want) {
		t.Errorf("before 应含 %s, 实际: %s", want, before)
	}

	var a packageSetAfter
	if err := json.Unmarshal(after, &a); err != nil {
		t.Fatalf("after_state 非法 JSON(%v): %s", err, after)
	}
	if a != (packageSetAfter{Name: "htop", DesiredState: "absent", Verb: "remove"}) {
		t.Errorf("after = %+v, want {htop absent remove}(verb 独立于 stub 布尔)", a)
	}
}

// TestPackageSet_Propose_RejectsBadArgs 钉"任何拒绝都发生在 rpm 查询之前"(todo 8
// 设计契约): 五种守卫出口(未知键/尾随数据/缺字段走解析门, 非法名走名称门,
// 表外值走三元组门)逐一 err 非 nil + 错误串逐字前缀 + before/after 双 nil +
// stub 调用计数恒 0。
func TestPackageSet_Propose_RejectsBadArgs(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantPrefix string
	}{
		{"unknown_field", `{"name":"htop","desired_state":"present","extra":"x"}`, "invalid package.set args: "},
		{"trailing_data", `{"name":"htop","desired_state":"present"}{"name":"nginx","desired_state":"absent"}`, "invalid package.set args: "},
		{"missing_desired_state", `{"name":"htop"}`, "invalid package.set args: "},
		{"bad_name", `{"name":"foo..bar","desired_state":"present"}`, "Invalid package name or pattern"},
		{"unknown_desired_state", `{"name":"htop","desired_state":"weird"}`, "unknown desired_state"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls int
			stubRpmQuery(t, func(context.Context, string) (bool, string, error) {
				calls++
				return true, "stub-must-not-run", nil
			})

			before, after, err := packageSetAdapter{}.Propose(context.Background(), json.RawMessage(tc.raw))
			if err == nil {
				t.Fatalf("非法 args 必须被拒: raw=%s", tc.raw)
			}
			if !strings.HasPrefix(err.Error(), tc.wantPrefix) {
				t.Errorf("错误串 = %q, want 以 %q 开头", err.Error(), tc.wantPrefix)
			}
			if before != nil || after != nil {
				t.Errorf("拒绝时双快照必须为 nil, got before=%s after=%s", before, after)
			}
			if calls != 0 {
				t.Errorf("守卫拒绝时 rpm 查询调用次数 = %d, want 0(查询必须排在全部守卫之后)", calls)
			}
		})
	}
}

// TestPackageSet_Propose_RpmQueryError 钉查询失败如实上抛: stub 抛错 → 同字
// 透传 error(不包装不吞)、双快照 nil、不 panic(本测试能正常返回即证不 panic)。
func TestPackageSet_Propose_RpmQueryError(t *testing.T) {
	stubErr := errors.New("rpm 查询执行失败: rpmdb locked")
	stubRpmQuery(t, func(context.Context, string) (bool, string, error) {
		return false, "", stubErr
	})

	before, after, err := packageSetAdapter{}.Propose(context.Background(),
		json.RawMessage(`{"name":"htop","desired_state":"present"}`))
	if err == nil {
		t.Fatal("rpm 查询失败必须原样上抛 error, 绝不谎报现状")
	}
	if err.Error() != stubErr.Error() {
		t.Errorf("错误串 = %q, want 同字透传 %q", err.Error(), stubErr.Error())
	}
	if before != nil || after != nil {
		t.Errorf("查询失败时双快照必须为 nil, got before=%s after=%s", before, after)
	}
}

// ─────────────────── todo 14 追加区: Apply 行为测试(勿改上方 todo 12/13 分区) ───────────────────

// applyTxID 是 happy 系 ctx 携带的事务 id: 16 位小写十六进制, 与 tx 日志的 id
// 形状门同形态 —— sidecar 三元组断言里 txID 分量要逐字回比对, 用真形状而非任意串。
const applyTxID = "0a1b2c3d4e5f6071"

// applyStubs 描述四条注入缝的桩行为, 零值即"全链路 happy"缺省:
// euid=0(root)、dnf 返零值 OpResult(rc=0 无输出)、history id 按 histID 回、
// read/write 两缝不注入失败。
type applyStubs struct {
	euid    int
	dnf     tx.OpResult
	histID  int64
	histErr error
	sideErr error
}

// applySpy 捕获四条缝被调用时的实参与计数 —— argv 逐字、sidecar 三元组逐值、
// 失败路径物理零调用三类契约全靠它断言。
type applySpy struct {
	dnfCalls   int
	dnfArgs    []string
	histCalls  int
	histVerb   string
	histName   string
	sideCalls  int
	sideTxID   string
	sideIndex  int
	sideHistID int64
}

// stubApplyDeps 一次性覆写 geteuid/dnfExecFn/readDnfHistoryIDFn/
// writeDnfHistorySidecarFn 四条包级 var(oracle C3 mock 注入契约的消费端),
// 每条缝各自登记 t.Cleanup 还原原值 —— 一缝一还原, 杜绝跨测试状态泄漏。
// 绝不绕过 var 直改原始函数(plan 348 行明文: 那样 mock 不会生效)。
func stubApplyDeps(t *testing.T, st applyStubs) *applySpy {
	t.Helper()
	spy := &applySpy{}

	origEuid := geteuid
	t.Cleanup(func() { geteuid = origEuid })
	geteuid = func() int { return st.euid }

	origDnf := dnfExecFn
	t.Cleanup(func() { dnfExecFn = origDnf })
	dnfExecFn = func(_ context.Context, args ...string) tx.OpResult {
		spy.dnfCalls++
		spy.dnfArgs = append([]string(nil), args...)
		return st.dnf
	}

	origHist := readDnfHistoryIDFn
	t.Cleanup(func() { readDnfHistoryIDFn = origHist })
	readDnfHistoryIDFn = func(_ context.Context, wantVerb, wantName string) (int64, error) {
		spy.histCalls++
		spy.histVerb, spy.histName = wantVerb, wantName
		if st.histErr != nil {
			return 0, st.histErr
		}
		return st.histID, nil
	}

	origSide := writeDnfHistorySidecarFn
	t.Cleanup(func() { writeDnfHistorySidecarFn = origSide })
	writeDnfHistorySidecarFn = func(txID string, stepIndex int, id int64) error {
		spy.sideCalls++
		spy.sideTxID, spy.sideIndex, spy.sideHistID = txID, stepIndex, id
		return st.sideErr
	}

	return spy
}

// applyArgsJSON 构造 step.Args 的线上形态(两键 schema, 名称门恒过的 htop)。
func applyArgsJSON(desiredState string) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"name":"htop","desired_state":%q}`, desiredState))
}

// runApplyHappy 驱动一次全 happy 路径的 Apply(euid=0 + dnf rc=0 带 Stdout +
// history id=42 + sidecar 写成功), ctx 经 withTxID 携带事务 id(D-1 通道);
// verb/三元组断言留在各测试函数自持。stepIndex 取互不相同的非零值, 证明
// sidecar 三元组第二分量确实搬运 step.Index 而非任何默认值。
func runApplyHappy(t *testing.T, desiredState string, stepIndex int) (tx.OpResult, *applySpy) {
	t.Helper()
	spy := stubApplyDeps(t, applyStubs{
		euid:   0,
		dnf:    tx.OpResult{Stdout: "Complete!"},
		histID: 42,
	})
	res := packageSetAdapter{}.Apply(withTxID(context.Background(), applyTxID),
		tx.Step{Index: stepIndex, Adapter: "package.set", Args: applyArgsJSON(desiredState)})
	return res, spy
}

// assertDnfArgv 钉 dnf 注入缝收到的 argv 逐字 = [verb, "-y", "htop"]
// (variadic 实参快照; %q 序列化对比, 失败信息直接可读)。
func assertDnfArgv(t *testing.T, spy *applySpy, verb string) {
	t.Helper()
	want := fmt.Sprintf("%q", []string{verb, "-y", "htop"})
	if got := fmt.Sprintf("%q", spy.dnfArgs); spy.dnfCalls != 1 || got != want {
		t.Fatalf("dnf 注入缝实参 = %s (calls=%d), want %s (calls=1)", got, spy.dnfCalls, want)
	}
}

// TestPackageSet_Apply_RejectsNonRoot 钉 root 守门(oracle C3 fix): geteuid 缝翻
// 1000 → rc=1 + 逐字锁定错串(全等断言天然蕴含 plan 要求的 "requires root" 子串),
// 且 dnf/history/sidecar 三条缝物理零调用 —— 守门排在一切副作用之前。
func TestPackageSet_Apply_RejectsNonRoot(t *testing.T) {
	spy := stubApplyDeps(t, applyStubs{euid: 1000, histID: 42})
	res := packageSetAdapter{}.Apply(withTxID(context.Background(), applyTxID),
		tx.Step{Index: 1, Adapter: "package.set", Args: applyArgsJSON("present")})
	if res.Returncode != 1 {
		t.Fatalf("非 root 必须 rc=1, got %+v", res)
	}
	if want := "package.set requires root (euid=0); re-run via sudo"; res.Error != want {
		t.Errorf("错误串 = %q, want 逐字 %q", res.Error, want)
	}
	if spy.dnfCalls != 0 || spy.histCalls != 0 || spy.sideCalls != 0 {
		t.Errorf("非 root 拒绝时三条副作用缝必须零调用, got dnf=%d hist=%d side=%d",
			spy.dnfCalls, spy.histCalls, spy.sideCalls)
	}
}

// TestPackageSet_Apply_HappyInstall 钉 present→install 全链路: rc=0 无 error、
// dnf Stdout 成功态原样透传(service.set Apply 同款惯例)、argv 逐字
// ["install","-y","htop"]、sidecar 三元组逐值 (applyTxID, step.Index=3, 42)。
func TestPackageSet_Apply_HappyInstall(t *testing.T) {
	res, spy := runApplyHappy(t, "present", 3)
	if res.Returncode != 0 || res.Error != "" {
		t.Fatalf("全 happy 链路必须 rc=0 无 error, got %+v", res)
	}
	if res.Stdout != "Complete!" {
		t.Errorf("dnf Stdout 应原样透传, got %q", res.Stdout)
	}
	assertDnfArgv(t, spy, "install")
	if spy.sideCalls != 1 || spy.sideTxID != applyTxID || spy.sideIndex != 3 || spy.sideHistID != 42 {
		t.Errorf("sidecar 三元组 = (%q, %d, %d) calls=%d, want (%q, 3, 42) calls=1",
			spy.sideTxID, spy.sideIndex, spy.sideHistID, spy.sideCalls, applyTxID)
	}
}

// TestPackageSet_Apply_HappyAbsent 钉 absent→remove 动词映射落到真实 argv。
func TestPackageSet_Apply_HappyAbsent(t *testing.T) {
	res, spy := runApplyHappy(t, "absent", 5)
	if res.Returncode != 0 || res.Error != "" {
		t.Fatalf("absent 全 happy 链路必须 rc=0 无 error, got %+v", res)
	}
	assertDnfArgv(t, spy, "remove")
	if spy.sideCalls != 1 || spy.sideIndex != 5 {
		t.Errorf("sidecar 必须落一次且 Index 分量搬运 step.Index=5, got calls=%d index=%d",
			spy.sideCalls, spy.sideIndex)
	}
}

// TestPackageSet_Apply_HappyLatest 钉 latest→upgrade 动词映射落到真实 argv。
func TestPackageSet_Apply_HappyLatest(t *testing.T) {
	res, spy := runApplyHappy(t, "latest", 7)
	if res.Returncode != 0 || res.Error != "" {
		t.Fatalf("latest 全 happy 链路必须 rc=0 无 error, got %+v", res)
	}
	assertDnfArgv(t, spy, "upgrade")
	if spy.sideCalls != 1 || spy.sideIndex != 7 {
		t.Errorf("sidecar 必须落一次且 Index 分量搬运 step.Index=7, got calls=%d index=%d",
			spy.sideCalls, spy.sideIndex)
	}
}

// TestPackageSet_Apply_RejectsBadDesiredState 钉三元组门在 Apply 侧重校验同样生效
// (euid 桩给 0, 排除"被 root 门误拒"的假绿): 错误串与 Propose 侧逐字同源,
// dnf 及后续两条缝零调用。
func TestPackageSet_Apply_RejectsBadDesiredState(t *testing.T) {
	spy := stubApplyDeps(t, applyStubs{euid: 0, histID: 42})
	res := packageSetAdapter{}.Apply(withTxID(context.Background(), applyTxID),
		tx.Step{Index: 1, Adapter: "package.set", Args: applyArgsJSON("weird")})
	if res.Returncode != 1 {
		t.Fatalf(`表外 desired_state "weird" 必须 rc=1, got %+v`, res)
	}
	if want := `unknown desired_state "weird" (v1 支持 present|absent|latest)`; res.Error != want {
		t.Errorf("错误串 = %q, want 逐字 %q", res.Error, want)
	}
	if !strings.Contains(res.Error, "unknown desired_state") {
		t.Errorf("错误串必须含 %q, got %q", "unknown desired_state", res.Error)
	}
	if spy.dnfCalls != 0 || spy.histCalls != 0 || spy.sideCalls != 0 {
		t.Errorf("守卫拒绝时三条副作用缝必须零调用, got dnf=%d hist=%d side=%d",
			spy.dnfCalls, spy.histCalls, spy.sideCalls)
	}
}

// TestPackageSet_Apply_DnfExecFails 钉 dnf 失败的原样透传: stub 返 rc=1 带
// Stderr → 返回的 OpResult 与 stub 值整体全等(rc/Stdout/Stderr/Error 一字不改),
// 且 read/sidecar 两条缝零调用 —— dnf 失败绝不进 sidecar 路径(review critical #1
// 守门: 失败的事务没有回滚材料可写)。
func TestPackageSet_Apply_DnfExecFails(t *testing.T) {
	boom := tx.OpResult{Returncode: 1, Stderr: "Error: Unable to find a match: htop"}
	spy := stubApplyDeps(t, applyStubs{euid: 0, dnf: boom, histID: 42})
	res := packageSetAdapter{}.Apply(withTxID(context.Background(), applyTxID),
		tx.Step{Index: 1, Adapter: "package.set", Args: applyArgsJSON("present")})
	if res != boom {
		t.Errorf("dnf 失败必须整 OpResult 原样透传, got %+v, want %+v", res, boom)
	}
	assertDnfArgv(t, spy, "install")
	if spy.histCalls != 0 {
		t.Errorf("dnf 失败后不得读取 history id, got histCalls=%d", spy.histCalls)
	}
	if spy.sideCalls != 0 {
		t.Errorf("dnf 失败后不得进 sidecar 路径(critical #1 守门), got sideCalls=%d", spy.sideCalls)
	}
}

// TestPackageSet_Apply_HistoryIDReadFails 钉 critical #1 第一失败支: history id
// 读取抛错 → rc=1 + 逐字前缀 "rollback unavailable: dnf history cannot be captured: "
// + 原始错误细节透传, 且 writeDnfHistorySidecarFn 计数器恒 0(plan 逐字要求 ——
// 拿不到 id 就绝不落 sidecar)。dnf 成功输出的 Stdout 保留在透传 res 上供审计。
func TestPackageSet_Apply_HistoryIDReadFails(t *testing.T) {
	spy := stubApplyDeps(t, applyStubs{
		euid:    0,
		dnf:     tx.OpResult{Stdout: "Complete!"},
		histErr: errors.New("dnf history 执行失败: rpmdb locked"),
	})
	res := packageSetAdapter{}.Apply(withTxID(context.Background(), applyTxID),
		tx.Step{Index: 1, Adapter: "package.set", Args: applyArgsJSON("present")})
	if res.Returncode != 1 {
		t.Fatalf("history id 读取失败必须整体判 Apply 失败, got %+v", res)
	}
	const prefix = "rollback unavailable: dnf history cannot be captured: "
	if !strings.HasPrefix(res.Error, prefix) {
		t.Errorf("错误串 = %q, want 以逐字前缀 %q 开头", res.Error, prefix)
	}
	if !strings.Contains(res.Error, "rpmdb locked") {
		t.Errorf("原始错误细节必须透传进错误串, got %q", res.Error)
	}
	if spy.sideCalls != 0 {
		t.Errorf("read 失败时 sidecar 缝必须未被调用(plan 逐字要求), got sideCalls=%d", spy.sideCalls)
	}
	if res.Stdout != "Complete!" {
		t.Errorf("dnf 成功输出应保留供审计, got Stdout=%q", res.Stdout)
	}
}

// TestPackageSet_Apply_SidecarWriteFails 钉 critical #1 第二失败支: sidecar 写
// 失败 → Returncode==1 + 逐字前缀 "rollback unavailable: dnf history sidecar
// write failed: " + **绝不返 success**(dnf 已实际改了系统, 但回滚材料没落盘,
// 谎报成功会制造不可回滚的事务)。
func TestPackageSet_Apply_SidecarWriteFails(t *testing.T) {
	spy := stubApplyDeps(t, applyStubs{
		euid:    0,
		dnf:     tx.OpResult{Stdout: "Complete!"},
		histID:  42,
		sideErr: errors.New("permission denied"),
	})
	res := packageSetAdapter{}.Apply(withTxID(context.Background(), applyTxID),
		tx.Step{Index: 1, Adapter: "package.set", Args: applyArgsJSON("present")})
	if res.Returncode != 1 {
		t.Fatalf("sidecar 写失败绝不返 success, got rc=%d error=%q", res.Returncode, res.Error)
	}
	const prefix = "rollback unavailable: dnf history sidecar write failed: "
	if !strings.HasPrefix(res.Error, prefix) {
		t.Errorf("错误串 = %q, want 以逐字前缀 %q 开头", res.Error, prefix)
	}
	if !strings.Contains(res.Error, "permission denied") {
		t.Errorf("原始错误细节必须透传进错误串, got %q", res.Error)
	}
	if spy.sideCalls != 1 {
		t.Errorf("sidecar 缝应恰被尝试一次, got sideCalls=%d", spy.sideCalls)
	}
}

// TestPackageSet_Apply_MissingTxIDCtx 【D-1 裁决新增第 9 测试, 不在 plan 8 函数
// 名单内 —— orchestrator D-1 指令要求把 todo 9 scratch case (f) 正式化, F1 审查
// 按 8+1 复核】: 裸 context.Background()(txID 未透传)→ rc=1 + 逐字锁定错串,
// 且 dnf 缝零调用 —— 守门位于一切 dnf 副作用之前(裁决第 3 条: 拿不到 txID
// 绝不先跑 dnf, 否则改造成不可回滚的既成事实)。euid 桩给 0, 排除被 root 门
// 误拒的假绿。
func TestPackageSet_Apply_MissingTxIDCtx(t *testing.T) {
	spy := stubApplyDeps(t, applyStubs{euid: 0, histID: 42})
	res := packageSetAdapter{}.Apply(context.Background(),
		tx.Step{Index: 1, Adapter: "package.set", Args: applyArgsJSON("present")})
	if res.Returncode != 1 {
		t.Fatalf("txID 缺失必须 fail-closed rc=1, got %+v", res)
	}
	if want := "rollback unavailable: tx id 未透传"; res.Error != want {
		t.Errorf("错误串 = %q, want 逐字 %q", res.Error, want)
	}
	if spy.dnfCalls != 0 || spy.histCalls != 0 || spy.sideCalls != 0 {
		t.Errorf("txID 守门必须排在 dnf 之前, got dnf=%d hist=%d side=%d",
			spy.dnfCalls, spy.histCalls, spy.sideCalls)
	}
}

// TestPackageSet_Apply_HistoryCmdlineMismatchFailClosed 钉【review M-1 fix】的
// 调用点契约: 真 readDnfHistoryID 在末行 Command line 与本次事务不符时抛
// "与本次事务命令不符" error(其自身行为由 package_set_exec_test.go 钉)——
// Apply 必须把本次实发的 verb 与包名透传给注入缝(桩侧断言收到
// wantVerb=="install"/wantName=="htop", 证明新参数确实从调用点传到位),
// 失败走 HistoryIDReadFails 同族通道: rc=1 + 逐字前缀 + sidecar 零调用。
func TestPackageSet_Apply_HistoryCmdlineMismatchFailClosed(t *testing.T) {
	spy := stubApplyDeps(t, applyStubs{
		euid: 0,
		dnf:  tx.OpResult{Stdout: "Complete!"},
		histErr: errors.New(
			`dnf history 末行 Command line "remove -y vim" 与本次事务命令不符(期望 install htop), 捕获不可信`),
	})
	res := packageSetAdapter{}.Apply(withTxID(context.Background(), applyTxID),
		tx.Step{Index: 1, Adapter: "package.set", Args: applyArgsJSON("present")})
	if res.Returncode != 1 {
		t.Fatalf("捕获不可信必须整体判 Apply 失败, got %+v", res)
	}
	const prefix = "rollback unavailable: dnf history cannot be captured: "
	if !strings.HasPrefix(res.Error, prefix) {
		t.Errorf("错误串 = %q, want 以逐字前缀 %q 开头", res.Error, prefix)
	}
	if !strings.Contains(res.Error, "与本次事务命令不符") {
		t.Errorf("相关性校验细节必须透传进错误串, got %q", res.Error)
	}
	if spy.histCalls != 1 || spy.histVerb != "install" || spy.histName != "htop" {
		t.Errorf("调用点必须把本次实发 verb/name 透传给捕获缝, got calls=%d verb=%q name=%q",
			spy.histCalls, spy.histVerb, spy.histName)
	}
	if spy.sideCalls != 0 {
		t.Errorf("捕获不可信时 sidecar 缝必须未被调用, got sideCalls=%d", spy.sideCalls)
	}
}

// ─────────────────── todo 15 追加区: Rollback 行为测试 + E2E 审计链(勿改上方 todo 12/13/14 分区) ───────────────────
//
// Rollback 守门次序即契约(package_set.go todo 10): 三连重校验 → geteuid →
// txIDFromCtx → readDnfHistorySidecar(真文件读)→ dnfHistoryExistsFn 探测 →
// 分流(undo 透传 / remove best-effort / absent 严格 error)。sidecar 读门排在
// 探测之前, 探测排在一切 dnf 动作之前 —— 各负例测试用对应缝的零调用计数钉死。
// 读侧不设 mock 缝(todo 10 裁决): 测试经 DAEDALUS_TX_DIR 指临时根后真写、真读,
// 往返形态更接近生产。

// isolateTxDir 把事务根指进隔离临时目录(dirs 覆盖链首位, 值必须绝对路径)。
// Rollback/E2E 测试一律先经本 helper, 杜绝 sidecar/tx journal 写进 $HOME 或系统路径。
func isolateTxDir(t *testing.T) {
	t.Helper()
	t.Setenv("DAEDALUS_TX_DIR", t.TempDir())
}

// seedSidecar 用真实 writeDnfHistorySidecar 预置回滚材料(与生产 readDnfHistorySidecar
// 同源同格式: `<txID>-<stepIndex>.dnf_history_id`, 内容为十进制裸 id)。直调原始函数
// 而非 var 缝: 覆写与否互不干扰, 真文件 IO 零系统副作用(todo 10 无 mock 缝裁决)。
func seedSidecar(t *testing.T, txID string, stepIndex int, id int64) {
	t.Helper()
	if err := writeDnfHistorySidecar(txID, stepIndex, id); err != nil {
		t.Fatalf("预置 sidecar 失败: %v", err)
	}
}

// existsSpy 捕获 dnfHistoryExistsFn 探测缝的调用计数与最近实参 ——
// "守门拒绝时不探测"(exists calls==0)与"探测收到 sidecar 真读 id"两类契约靠它断言。
type existsSpy struct {
	calls  int
	lastID int64
}

// stubRollbackExists 覆写第六道注入缝 dnfHistoryExistsFn(stubApplyDeps 的四缝组合
// 不含探测缝, Rollback 区单独另覆), 返回固定探测行为 + t.Cleanup 还原原值。
func stubRollbackExists(t *testing.T, exists bool, probeErr error) *existsSpy {
	t.Helper()
	spy := &existsSpy{}
	orig := dnfHistoryExistsFn
	t.Cleanup(func() { dnfHistoryExistsFn = orig })
	dnfHistoryExistsFn = func(_ context.Context, id int64) (bool, error) {
		spy.calls++
		spy.lastID = id
		return exists, probeErr
	}
	return spy
}

// runRollback 以给定 ctx 驱动一次 Rollback(ctx 是否为 withTxID 产物由各测试自持,
// 与生产 cmdRollback 的 D-1 注入点同通道)。
func runRollback(ctx context.Context, desiredState string, stepIndex int) tx.OpResult {
	return packageSetAdapter{}.Rollback(ctx,
		tx.Step{Index: stepIndex, Adapter: "package.set", Args: applyArgsJSON(desiredState)})
}

// assertUndoArgv 钉 dnf 注入缝实参逐字 = [history, undo, -y, 42] —— "42" 只可能
// 来自真 sidecar 往返(readDnfHistorySidecar 无 mock 缝), 本断言顺带钉读侧解析。
func assertUndoArgv(t *testing.T, spy *applySpy) {
	t.Helper()
	want := fmt.Sprintf("%q", []string{"history", "undo", "-y", "42"})
	if got := fmt.Sprintf("%q", spy.dnfArgs); spy.dnfCalls != 1 || got != want {
		t.Fatalf("dnf 注入缝实参 = %s (calls=%d), want %s (calls=1)", got, spy.dnfCalls, want)
	}
}

// isHex64 报告 s 是否恰为 64 位小写十六进制(entry_hash/prev_hash 的形状契约)。
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	return strings.IndexFunc(s, func(r rune) bool {
		return !strings.ContainsRune("0123456789abcdef", r)
	}) < 0
}

// TestPackageSet_Rollback_HappyUndo 钉 undo 主分支: 真 sidecar(id=42)+ 探测
// exists=true → argv 逐字 ["history","undo","-y","42"], dnf 结果整 OpResult 原样
// 透传, 探测收到 sidecar 真读 id, sidecar 写缝零调用(纯消费侧契约)。
func TestPackageSet_Rollback_HappyUndo(t *testing.T) {
	isolateTxDir(t)
	seedSidecar(t, applyTxID, 2, 42)
	want := tx.OpResult{Stdout: "Undone"}
	spy := stubApplyDeps(t, applyStubs{euid: 0, dnf: want})
	ex := stubRollbackExists(t, true, nil)

	res := runRollback(withTxID(context.Background(), applyTxID), "present", 2)
	if res != want {
		t.Errorf("undo 结果必须整 OpResult 原样透传, got %+v, want %+v", res, want)
	}
	assertUndoArgv(t, spy)
	if ex.calls != 1 || ex.lastID != 42 {
		t.Errorf("探测缝 = calls %d id %d, want 1 次且 id=42(来自 sidecar 真读)", ex.calls, ex.lastID)
	}
	if spy.sideCalls != 0 {
		t.Errorf("Rollback 是纯消费侧, sidecar 写必须零调用, got sideCalls=%d", spy.sideCalls)
	}
}

// TestPackageSet_Rollback_BestEffortAfterHistoryCleanup 钉 history 清理后的
// best-effort 分流: 探测 exists=false + desired_state=present/latest 两态均走
// remove -y htop 兜底(装回来的东西直接卸掉即回到 absent 现状), rc=0 成功透传。
func TestPackageSet_Rollback_BestEffortAfterHistoryCleanup(t *testing.T) {
	for _, state := range []string{"present", "latest"} {
		t.Run(state, func(t *testing.T) {
			isolateTxDir(t)
			seedSidecar(t, applyTxID, 1, 42)
			spy := stubApplyDeps(t, applyStubs{euid: 0, dnf: tx.OpResult{Stdout: "Complete!"}})
			ex := stubRollbackExists(t, false, nil)

			res := runRollback(withTxID(context.Background(), applyTxID), state, 1)
			if res.Returncode != 0 || res.Error != "" {
				t.Fatalf("兜底成功必须 rc=0 无 error, got %+v", res)
			}
			assertDnfArgv(t, spy, "remove")
			if ex.calls != 1 || ex.lastID != 42 {
				t.Errorf("探测缝 = calls %d id %d, want 1 次且 id=42(先探测再分流)", ex.calls, ex.lastID)
			}
			if spy.sideCalls != 0 {
				t.Errorf("兜底分支同样不得写 sidecar, got sideCalls=%d", spy.sideCalls)
			}
		})
	}
}

// TestPackageSet_Rollback_AbsentNoFallback 钉【oracle critical #1】守门:
// exists=false + absent → 严格 error、rc=1、逐字长串(全等断言), dnf 缝物理零调用 ——
// dnf remove 对已 absent 包是空操作, 兜底会把"什么都没回滚"误报成回滚成功。
// dnf 桩故意返回 rc=0: 若分流被改坏, 调用计数断言必然 first 失败。
func TestPackageSet_Rollback_AbsentNoFallback(t *testing.T) {
	isolateTxDir(t)
	seedSidecar(t, applyTxID, 0, 42)
	spy := stubApplyDeps(t, applyStubs{euid: 0, dnf: tx.OpResult{Stdout: "must-not-run"}})
	stubRollbackExists(t, false, nil)

	res := runRollback(withTxID(context.Background(), applyTxID), "absent", 0)
	if res.Returncode != 1 {
		t.Fatalf("absent 不可逆必须 rc=1, got %+v", res)
	}
	if want := "rollback 不可逆：desired_state=absent 的 rollback 在 dnf history 清理后无可靠回滚手段（dnf remove 对已 absent 包是空操作会误报 success，dnf install 又需要版本信息；强制要求人工处理）"; res.Error != want {
		t.Errorf("错误串 = %q, want 逐字 %q", res.Error, want)
	}
	if spy.dnfCalls != 0 {
		t.Errorf("absent 分支 dnf 必须物理零调用(C1 守门: 空操作兜底=误报 success), got dnfCalls=%d", spy.dnfCalls)
	}
	if spy.sideCalls != 0 {
		t.Errorf("absent 分支不得写 sidecar, got sideCalls=%d", spy.sideCalls)
	}
}

// TestPackageSet_Rollback_RejectsNonRoot 钉 root 守门: geteuid 缝翻 1000 → rc=1 +
// 逐字错串(与 Apply 侧同源), 且探测/dnf/写 sidecar 各缝物理零调用 —— 守门排在
// sidecar 真读与一切副作用之前。sidecar 已预置, 排除"因缺文件被拒"的假绿。
func TestPackageSet_Rollback_RejectsNonRoot(t *testing.T) {
	isolateTxDir(t)
	seedSidecar(t, applyTxID, 1, 42)
	spy := stubApplyDeps(t, applyStubs{euid: 1000})
	ex := stubRollbackExists(t, true, nil)

	res := runRollback(withTxID(context.Background(), applyTxID), "present", 1)
	if res.Returncode != 1 {
		t.Fatalf("非 root 必须 rc=1, got %+v", res)
	}
	if want := "package.set requires root (euid=0); re-run via sudo"; res.Error != want {
		t.Errorf("错误串 = %q, want 逐字 %q", res.Error, want)
	}
	if spy.dnfCalls != 0 || ex.calls != 0 || spy.sideCalls != 0 {
		t.Errorf("非 root 拒绝时探测/dnf/sidecar 缝必须零调用, got dnf=%d exists=%d side=%d",
			spy.dnfCalls, ex.calls, spy.sideCalls)
	}
}

// TestPackageSet_Rollback_NoSidecar 钉【review important #3】: sidecar 文件缺失 →
// 严格 rc=1 + Error 逐字(不插值)、读失败细节走 Stderr 诊断通道, 且**不探测、
// 不兜底**(exists/dnf 双零调用)——回滚材料不存在即到此为止。
func TestPackageSet_Rollback_NoSidecar(t *testing.T) {
	isolateTxDir(t) // 刻意不 seed: 本 case 钉的就是文件不存在态
	spy := stubApplyDeps(t, applyStubs{euid: 0, dnf: tx.OpResult{Stdout: "must-not-run"}})
	ex := stubRollbackExists(t, true, nil)

	res := runRollback(withTxID(context.Background(), applyTxID), "present", 9)
	if res.Returncode != 1 {
		t.Fatalf("sidecar 缺失必须 fail-closed rc=1, got %+v", res)
	}
	if want := "rollback 缺少 dnf history id（sidecar 不存在或不可读）"; res.Error != want {
		t.Errorf("错误串 = %q, want 逐字 %q", res.Error, want)
	}
	if res.Stderr == "" {
		t.Errorf("读失败细节必须走 Stderr 诊断通道(Error 保持逐字), got 空")
	}
	if ex.calls != 0 {
		t.Errorf("sidecar 缺失时不得探测 history(读门排在探测之前), got existsCalls=%d", ex.calls)
	}
	if spy.dnfCalls != 0 {
		t.Errorf("sidecar 缺失时不得兜底动 dnf(review important #3), got dnfCalls=%d", spy.dnfCalls)
	}
}

// TestPackageSet_Rollback_HistoryUndoFails 钉 undo 失败透传: dnf 返 rc=1 带
// Stderr → 整 OpResult 全等原样透传(不吞错不改写), argv 仍是 undo 四段。
func TestPackageSet_Rollback_HistoryUndoFails(t *testing.T) {
	isolateTxDir(t)
	seedSidecar(t, applyTxID, 1, 42)
	boom := tx.OpResult{Returncode: 1, Stderr: "conflicts with installed package"}
	spy := stubApplyDeps(t, applyStubs{euid: 0, dnf: boom})
	stubRollbackExists(t, true, nil)

	res := runRollback(withTxID(context.Background(), applyTxID), "present", 1)
	if res != boom {
		t.Errorf("undo 失败必须整 OpResult 原样透传(不吞), got %+v, want %+v", res, boom)
	}
	assertUndoArgv(t, spy)
}

// TestPackageSet_Rollback_BestEffortFails 钉兜底失败形态: exists=false + present
// + remove rc=1 → Error 为两段拼接(全等断言天然蕴含 plan 要求的 "rollback 依赖的
// dnf history 已清理" 前缀与 remove 兜底失败细节 "rpmdb locked", id 插值逐字),
// rc 保持透传值 1。
func TestPackageSet_Rollback_BestEffortFails(t *testing.T) {
	isolateTxDir(t)
	seedSidecar(t, applyTxID, 1, 42)
	spy := stubApplyDeps(t, applyStubs{euid: 0, dnf: tx.OpResult{Returncode: 1, Stderr: "rpmdb locked"}})
	stubRollbackExists(t, false, nil)

	res := runRollback(withTxID(context.Background(), applyTxID), "present", 1)
	if res.Returncode != 1 {
		t.Fatalf("兜底失败必须 rc=1, got %+v", res)
	}
	if want := "rollback 依赖的 dnf history 已清理（id=42），且 remove 兜底失败：rpmdb locked"; res.Error != want {
		t.Errorf("错误串 = %q, want 逐字 %q", res.Error, want)
	}
	assertDnfArgv(t, spy, "remove")
}

// TestPackageSet_Rollback_MissingTxIDCtx 【D-1 对称守门, 非 plan 7 函数名单 ——
// orchestrator §2 指令按 7+1+1 复核, 与 Apply 侧同名 case 对称】: 裸 ctx → rc=1 +
// 逐字串, 探测/dnf/sidecar 各缝零调用 —— txID 守门排在 sidecar 读之前, 读之后
// 一切下游皆不该发生。euid 桩给 0 排除被 root 门误拒的假绿; sidecar 预置证明
// 拒因是 ctx 缺 txID 而非文件缺失。
func TestPackageSet_Rollback_MissingTxIDCtx(t *testing.T) {
	isolateTxDir(t)
	seedSidecar(t, applyTxID, 1, 42)
	spy := stubApplyDeps(t, applyStubs{euid: 0})
	ex := stubRollbackExists(t, true, nil)

	res := runRollback(context.Background(), "present", 1)
	if res.Returncode != 1 {
		t.Fatalf("txID 缺失必须 fail-closed rc=1, got %+v", res)
	}
	if want := "rollback unavailable: tx id 未透传"; res.Error != want {
		t.Errorf("错误串 = %q, want 逐字 %q", res.Error, want)
	}
	if spy.dnfCalls != 0 || ex.calls != 0 || spy.sideCalls != 0 {
		t.Errorf("txID 守门必须排在 sidecar 读与探测之前, got dnf=%d exists=%d side=%d",
			spy.dnfCalls, ex.calls, spy.sideCalls)
	}
}

// TestPackageSet_EndToEnd_AuditChain 钉【review minor #7 / oracle I4】端到端证据链:
// 真实 JSONL 审计文件(不 mock audit 子系统)、cmdBegin→cmdPropose→cmdApply→cmdRollback
// 四步 exit code 全 0、audit.Verify 全链回放无错、链长 ≥4、in-tx 条目 TxID 全一致、
// 末条 prev_hash 与倒数第二条 entry_hash 衔接(64 hex 形状)。
//
// 【orchestrator 对 plan 373 行的裁决记录】plan 写"mock writeDnfHistorySidecarFn 返
// fake id"——**不采纳**: mock 掉写缝后 Rollback 的真 readDnfHistorySidecar 读不到
// 文件、进程内链断。本测试不覆写该缝, Apply 真写 sidecar 到 DAEDALUS_TX_DIR 临时根
// (纯文件 IO 零系统副作用), Rollback 真读 —— dnf 缝实参逐字出现 "history undo -y 42"
// 即往返成功的铁证(42 只能来自真文件, 且探测桩只认 id==42)。
//
// 探测 error 三态(dnfHistoryExistsFn 返 err)的错串形态 plan 未授权逐字钉,
// todo 10 移交注记"待 F1 复核"维持 —— 本文件不新增该形态断言。
func TestPackageSet_EndToEnd_AuditChain(t *testing.T) {
	isolateTxDir(t)
	logPath := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv(audit.EnvLogPath, logPath)

	// mock 缝清单五条: root 门 / rpm 现状 / dnf 进程 / history id 捕获 / history 探测。
	// writeDnfHistorySidecarFn 与 readDnfHistorySidecar 不设防(后者本无 var 缝): 真往返。
	origEuid := geteuid
	t.Cleanup(func() { geteuid = origEuid })
	geteuid = func() int { return 0 }

	origRpm := rpmQueryFn
	t.Cleanup(func() { rpmQueryFn = origRpm })
	rpmQueryFn = func(context.Context, string) (bool, string, error) { return false, "", nil }

	var dnfArgvs []string
	origDnf := dnfExecFn
	t.Cleanup(func() { dnfExecFn = origDnf })
	dnfExecFn = func(_ context.Context, args ...string) tx.OpResult {
		dnfArgvs = append(dnfArgvs, strings.Join(args, " "))
		return tx.OpResult{Stdout: "Complete!"}
	}

	origHist := readDnfHistoryIDFn
	t.Cleanup(func() { readDnfHistoryIDFn = origHist })
	readDnfHistoryIDFn = func(context.Context, string, string) (int64, error) { return 42, nil }

	origEx := dnfHistoryExistsFn
	t.Cleanup(func() { dnfHistoryExistsFn = origEx })
	dnfHistoryExistsFn = func(_ context.Context, id int64) (bool, error) { return id == 42, nil }

	// 驱动链: 直接调四个子命令编排函数(与生产同进程同函数, D-1 注入点走真通道)。
	var out, errBuf bytes.Buffer
	if code := cmdBegin(&out, &errBuf); code != exitOK {
		t.Fatalf("begin 退出码 = %d; stderr: %s", code, errBuf.String())
	}
	var doc beginDoc
	if err := json.Unmarshal(bytes.TrimSpace(out.Bytes()), &doc); err != nil {
		t.Fatalf("begin stdout 非 beginDoc JSON: %v (%q)", err, out.String())
	}
	txID := doc.TxID

	out.Reset()
	errBuf.Reset()
	if code := cmdPropose([]string{txID, "package.set", string(applyArgsJSON("present"))}, &out, &errBuf); code != exitOK {
		t.Fatalf("propose 退出码 = %d; stderr: %s", code, errBuf.String())
	}
	out.Reset()
	errBuf.Reset()
	if code := cmdApply([]string{txID}, &out, &errBuf); code != exitOK {
		t.Fatalf("apply 退出码 = %d; stderr: %s", code, errBuf.String())
	}
	out.Reset()
	errBuf.Reset()
	if code := cmdRollback([]string{txID}, &out, &errBuf); code != exitOK {
		t.Fatalf("rollback 退出码 = %d; stderr: %s", code, errBuf.String())
	}

	// dnf 进程缝观测: install 实发 + undo 带真读回的 42(sidecar 往返铁证)。
	if len(dnfArgvs) != 2 || dnfArgvs[0] != "install -y htop" || dnfArgvs[1] != "history undo -y 42" {
		t.Fatalf("dnf 缝调用序列 = %q, want [install -y htop, history undo -y 42]", dnfArgvs)
	}

	// 审计文件存在 + 全链 Verify 回放(全局链 + 逐事务双链, 含创世播种)。
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("审计日志未生成: %v", err)
	}
	count, err := audit.Verify(logPath)
	if err != nil {
		t.Fatalf("audit.Verify 回放失败: %v", err)
	}
	if count < 4 {
		t.Fatalf("审计链长 = %d, want ≥ 4(begin + propose 外围 + apply + rollback)", count)
	}

	// 逐行复读: in-tx 条目恰 begin/apply/rollback 三条且 TxID 全等于 begin 返的 id
	// (propose 按盖章规则发空 TxID); 末条 prev_hash 64 hex 且与倒数第二条
	// entry_hash 衔接(Verify 规则 3 已覆盖全局连续性, 此处对末条冗余复钉形状)。
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("读审计日志失败: %v", err)
	}
	type auditLine struct {
		TxID      string `json:"tx_id"`
		PrevHash  string `json:"prev_hash"`
		EntryHash string `json:"entry_hash"`
	}
	var recs []auditLine
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if line == "" {
			continue
		}
		var r auditLine
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			t.Fatalf("审计行非法 JSON: %v (%q)", err, line)
		}
		recs = append(recs, r)
	}
	var inTx int
	for i, r := range recs {
		if r.TxID == "" {
			continue
		}
		inTx++
		if r.TxID != txID {
			t.Errorf("第 %d 条 in-tx 条目 TxID = %q, want 恒等于 begin 返的 %q", i+1, r.TxID, txID)
		}
	}
	if inTx != 3 {
		t.Errorf("in-tx 条目数 = %d, want 3(begin + apply + rollback, propose 发空 TxID)", inTx)
	}
	last, prev := recs[len(recs)-1], recs[len(recs)-2]
	if !isHex64(last.PrevHash) {
		t.Errorf("末条 prev_hash 非 64 位小写十六进制: %q", last.PrevHash)
	}
	if last.PrevHash != prev.EntryHash {
		t.Errorf("末条 prev_hash = %s, want 衔接倒数第二条 entry_hash %s", last.PrevHash, prev.EntryHash)
	}
}
