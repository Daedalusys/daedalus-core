package main

// cmdRescue 实现 daedalus-atomic headless 镜像的"一键救砖"子命令
// (enable-vnc | disable-vnc | status)。
//
// ★ 边界:仅在 headless 变体内执行(VARIANT=headless 标记存在时)。
// 标记缺失 → 非零退出 + stderr 报错;防止在普通 AlmaLinux host 上误把
// xrdp unmask + 把 3389 暴露给生产网。
//
// ★ startwm.sh 兜底:T1 把 /etc/xrdp/startwm.sh 写成
// `exec dbus-launch --exit-with-session startplasma-x11`,但 headless 镜像按
// spec §4.5 不装 plasma。enable-vnc 必须先把 startwm.sh 覆写为最小可用 WM
// (xterm — xorg-x11-xinit / xrdp 包组里必装,无需新依赖),否则 sesman 拉 Xorg
// → startwm.sh 跑 startplasma-x11: command not found → X session failed →
// 3389 仍开但 VNC 不可用,救砖失效。disable-vnc 选择保留 xterm 兜底 stub:
// 它无害,且阻止后续 KDE 风格 startwm.sh 复活造成同类故障。
//
// ★ xrdp 按需安装(2026-10-22 真机修):headless 镜像**根本不装 xrdp**
// (files/scripts/77-xrdp.sh 在 VARIANT=headless 时第一行就 exit 0),而本文件
// 原实现直接 `unmask xrdp` + 写 /etc/xrdp/startwm.sh —— 在真机上必然报
// `open /etc/xrdp/startwm.sh: no such file or directory` 并退出 1,救砖路径整个是断的。
// 现在 enable-vnc 先 ensureXrdpInstalled() 按需装(EPEL → COPR mlampe/xrdp,
// 与 77-xrdp.sh 同一套仓库来源与回退顺序),再走原来的 unmask/enable 流程。

import (
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"
)

// rescueState 是 status 子命令的输出契约 + detectRescueState 的返回类型,
// 字段名固定(测试按字面断言)。
type rescueState struct {
	// XrdpUnitState 取值:"masked" | "disabled" | "enabled" | "running" |
	// "not-installed" | "unknown"。
	//
	// ★ "not-installed" 是 2026-10-22 真机修新增的取值:headless 镜像不带 xrdp,
	// 此时 `systemctl show xrdp` 仍返回 LoadState=not-found(命令本身成功),原代码
	// 把它映射成 "disabled" —— 排障时会误判成"装了只是没启用",实际是没装。
	XrdpUnitState string
	Port3389Open  bool // 3389/tcp 是否监听(本机 127.0.0.1)
	IsHeadless    bool // /etc/os-release 含 VARIANT=headless
}

// ★ 本文件四个路径变量(startwmPath / sesmanIniPath / coprRepoPath /
// xrdpBinPath)声明为 var 而非 const,只为让测试能指向 t.TempDir()。生产值是
// 字面量常量,不接受任何外部输入,无路径穿越面;若日后要从配置或参数注入路径,
// 必须先过一遍白名单校验。
const osReleasePath = "/etc/os-release"

// startwmPath 是 xrdp 默认 startwm.sh 路径。enable-vnc 必须覆写它。
var startwmPath = "/etc/xrdp/startwm.sh"

// startwmStub 是最小可用的 xterm-only startwm.sh 模板;enable-vnc 用 cat heredoc
// 写入(保证 xterm 路径在 PATH 中且 shell 不解释 `$`)。
const startwmStub = `#!/bin/sh
[ -r /etc/profile.d/lang.sh ] && . /etc/profile.d/lang.sh
exec xterm
`

// detectRescueState 把状态探测逻辑收拢到一处,供 cmdRescue 与测试共用。
//   - IsHeadless:扫 /etc/os-release 看是否含 VARIANT=headless;
//   - XrdpUnitState:`systemctl show xrdp -p LoadState,SubState,ActiveState` 串解析;
//     失败兜底 "unknown",保证字段非空;
//   - Port3389Open:对 127.0.0.1:3389 做 TCP dial(本机视角,firewalld 不参与)。
func detectRescueState() rescueState {
	st := rescueState{XrdpUnitState: "unknown"}
	st.IsHeadless = isHeadlessVariant(osReleasePath)
	if u, ok := probeXrdpUnitState(); ok {
		st.XrdpUnitState = u
	}
	st.Port3389Open = probePort3389()
	return st
}

// isHeadlessVariant:读 /etc/os-release,逐行查 `VARIANT=headless`。
// 文件不存在或不含该键 → false;真命中 → true。
func isHeadlessVariant(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "VARIANT=headless" {
			return true
		}
	}
	return false
}

// mapXrdpUnitState:(LoadState, SubState, ActiveState) 三元组 → 单一状态串。
// 抽成纯函数是为了能在没装 systemd 的 CI 上直接表驱动测试 —— 真机上这三条
// 的组合由 probeXrdpUnitState 从 `systemctl show` 解析而来。
func mapXrdpUnitState(load, sub, act string) (string, bool) {
	switch {
	case load == "not-found":
		return "not-installed", true
	case load == "masked":
		return "masked", true
	case act == "active" && sub == "running":
		return "running", true
	case act == "active":
		return "enabled", true
	default:
		if load != "" {
			return load, true
		}
		return "unknown", false
	}
}

// probeXrdpUnitState:`systemctl show xrdp -p LoadState -p SubState -p ActiveState`
// 三件套合成一个最终状态串。
//
// ★ 重要事实(2026-10-22 真机校准):unit 文件不存在时 `systemctl show` **仍然返回
// 0**,并输出 `LoadState=not-found` / `ActiveState=inactive` / `SubState=dead`。
// 所以“xrdp 没装”走不到 err != nil 分支,必须在 mapXrdpUnitState 里单独映射;
// 原代码把它当 "disabled",与本函数首部旧注释(“unit 不存在 → unknown”)矛盾,
// 在真机上会把排障方向带偏(让人以为装了只是没启用)。
// 真正的 unknown 只留给 systemctl 缺失 / show 调用失败。
func probeXrdpUnitState() (string, bool) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return "unknown", false
	}
	out, err := exec.Command("systemctl", "show", "xrdp",
		"-p", "LoadState", "-p", "SubState", "-p", "ActiveState").CombinedOutput()
	if err != nil {
		return "unknown", false
	}
	fields := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if eq := strings.IndexByte(line, '='); eq > 0 {
			fields[line[:eq]] = line[eq+1:]
		}
	}
	return mapXrdpUnitState(fields["LoadState"], fields["SubState"], fields["ActiveState"])
}

// probePort3389:对 127.0.0.1:3389 做 TCP dial,300ms 超时。监听即 open=true。
func probePort3389() bool {
	c, err := net.DialTimeout("tcp", "127.0.0.1:3389", 300*time.Millisecond)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// cmdRescue 派发到 enable-vnc / disable-vnc / status 三子命令。
// 派发前先做 IsHeadless 闸门:非 headless 直接非零退出 + stderr 报错(测试与
// 生产同源)。
func cmdRescue(stdout, stderr io.Writer, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: daedalus-host rescue {enable-vnc|disable-vnc|status}")
		return exitUsage
	}
	state := detectRescueState()
	if !state.IsHeadless {
		fmt.Fprintln(stderr, "rescue: only on daedalus-atomic headless variant")
		return exitRuntime
	}
	switch args[0] {
	case "enable-vnc":
		return rescueEnableVnc(stdout, stderr, state)
	case "disable-vnc":
		return rescueDisableVnc(stdout, stderr, state)
	case "status":
		return rescueStatus(stdout, stderr, state)
	default:
		fmt.Fprintln(stderr, "rescue: unknown subcommand:", args[0])
		return exitUsage
	}
}

// rescueEnableVnc:按需装 xrdp → 落 startwm.sh 兜底 stub → unmask → running,
// 写 firewall 规则,报最终状态。任一步失败 stderr 报原因并返回 exitRuntime;
// 全部成功 → exitOK。
//
// 顺序很要紧:装包必须排在写 startwm.sh / unmask 之前 —— /etc/xrdp 目录由 xrdp
// 包创建,包不在时写文件必然 ENOENT(这正是真机上的原始故障)。
func rescueEnableVnc(stdout, stderr io.Writer, state rescueState) int {
	// ★ 刻意**不**用 state.XrdpUnitState 判断要不要装包(真机踩过):disable-vnc
	// 跑过一次 mask 之后,`systemctl show xrdp` 报的是 masked —— 哪怕包根本没装
	// (mask 的语义就是“不管这个 unit 存不存在一律拦”)。拿 unit 状态当安装标志,
	// 会得到“装过了,跳过安装” → 紧接着写 /etc/xrdp/startwm.sh 仍然 ENOENT。
	// 装没装只看包落下的文件,见 xrdpInstalled。
	if !xrdpInstalled() {
		fmt.Fprintln(stderr, "rescue: xrdp 未安装(headless 镜像不含 RDP 包),正在按需安装 …")
		if err := ensureXrdpInstalled(stderr); err != nil {
			fmt.Fprintln(stderr, "rescue: xrdp 安装失败:", err)
			fmt.Fprintln(stderr, "rescue: 备选路径 = 串口控制台 / sshd(两者不依赖 xrdp)")
			return exitRuntime
		}
	}
	if err := hardenSesmanDepth(stderr); err != nil {
		// 非致命:新装的 xrdp 包 sesman.ini 默认就带 depth;改不动不该阻断救砖。
		fmt.Fprintln(stderr, "rescue: WARN sesman.ini depth 未调整:", err)
	}
	if err := writeStartwmStub(startwmPath); err != nil {
		fmt.Fprintln(stderr, "rescue: 写 startwm.sh 失败:", err)
		return exitRuntime
	}
	if err := runSystemctl(stderr, "unmask", "xrdp", "xrdp-sesman"); err != nil {
		fmt.Fprintln(stderr, "rescue: systemctl unmask 失败:", err)
		return exitRuntime
	}
	if err := runSystemctl(stderr, "enable", "--now", "xrdp", "xrdp-sesman"); err != nil {
		fmt.Fprintln(stderr, "rescue: systemctl enable --now 失败:", err)
		return exitRuntime
	}
	if err := openFirewallPort(stderr, "3389/tcp"); err != nil {
		// firewall 失败非致命(开发 / off-line 镜像可能没 firewalld):stderr 告警,
		// 不阻断救援流程,以免已拉起的 xrdp 反而被回滚。
		fmt.Fprintln(stderr, "rescue: WARN firewall 端口未开:", err)
	}
	fmt.Fprintln(stdout, "xrdp: enabled")
	fmt.Fprintln(stdout, "3389/tcp: open (firewall 已尝试)")
	return exitOK
}

// rescueDisableVnc:停 + 重新 mask xrdp,撤 firewall 端口。startwm.sh 保留
// xterm stub(无害且阻止 KDE 风格 startwm.sh 复活造成同类故障)。
//
// ★ 幂等(2026-10-22 真机修):本机从未开过 VNC 时 `systemctl disable --now xrdp`
// 会报 `Failed to disable unit: Unit xrdp.service does not exist` 并退出 1 —— 而
// “本就没开”恰恰是 disable-vnc 想达到的状态,报错是自相矛盾。此时降级为提示。
func rescueDisableVnc(stdout, stderr io.Writer, _ rescueState) int {
	out, err := execOut("systemctl", "disable", "--now", "xrdp", "xrdp-sesman")
	if err != nil {
		if unitMissing(out) {
			fmt.Fprintln(stderr, "rescue: xrdp 本就未启用,跳过 disable")
		} else {
			fmt.Fprintf(stderr, "rescue: systemctl disable --now 失败: %v\n%s\n", err, out)
			return exitRuntime
		}
	}
	if err := runSystemctl(stderr, "mask", "xrdp", "xrdp-sesman"); err != nil {
		fmt.Fprintln(stderr, "rescue: systemctl mask 失败:", err)
		return exitRuntime
	}
	if err := closeFirewallPort(stderr, "3389/tcp"); err != nil {
		fmt.Fprintln(stderr, "rescue: WARN firewall 端口未撤:", err)
	}
	fmt.Fprintln(stdout, "xrdp: masked")
	fmt.Fprintln(stdout, "3389/tcp: closed (firewall 已尝试)")
	return exitOK
}

// rescueStatus:打印 xrdp unit state + 3389 端口状态。前两行结构固定:
// 首行 "xrdp: <state>",第二行 "3389/tcp: open|closed"。
// not-installed 时补一行指引 —— 排障时最需要的就是这句(原实现把它误报成
// disabled,把人往“去 enable 一下”方向带偏)。
func rescueStatus(stdout, _ io.Writer, state rescueState) int {
	port := "closed"
	if state.Port3389Open {
		port = "open"
	}
	fmt.Fprintf(stdout, "xrdp: %s\n", state.XrdpUnitState)
	fmt.Fprintf(stdout, "3389/tcp: %s\n", port)
	if state.XrdpUnitState == "not-installed" {
		fmt.Fprintln(stdout, "hint: headless 镜像不含 xrdp;需要 VNC 救砖跑 `daedalus-host rescue enable-vnc`(会按需装包)")
	}
	return exitOK
}

// writeStartwmStub:heredoc 写 startwm.sh 并 chmod 0755。失败 → 报 stderr 由
// 调用方决定是否回滚 rescue。
func writeStartwmStub(path string) error {
	if err := os.WriteFile(path, []byte(startwmStub), 0o755); err != nil {
		return err
	}
	return os.Chmod(path, 0o755)
}

// --- 按需安装 xrdp(2026-10-22 真机修新增)-----------------------------------
//
// 仓库来源与回退顺序跟 files/scripts/77-xrdp.sh 保持一致(那是构建期装 xrdp 的
// 唯一已验证路径):EPEL → COPR mlampe/xrdp → 失败即报错。
// **不引入第三种来源**,避免运维要记三套仓库配置。

// coprRepoPath 是 rescue 写下的 COPR repo 文件。与 77-xrdp.sh 里的
// _copr_mlampe-xrdp.repo 同名同路径风格(下划线前缀标记为本地附加)。
var coprRepoPath = "/etc/yum.repos.d/_daedalus-xrdp.repo"

// coprRepoBody 必须**逐字**保留 $releasever —— 交给 dnf 展开。早先 77-xrdp.sh
// 用 quoted heredoc('REPO')避开 shell 展开,Go 侧同理不能用 fmt.Sprintf 拼。
// chroot 名是 rhel+epel-10-x86_64(不是 epel-10-x86_64):该 COPR 项目的实际命名。
const coprRepoBody = `[mlampe-xrdp]
name=Copr repo for xrdp owned by mlampe
baseurl=https://download.copr.fedorainfracloud.org/results/mlampe/xrdp/rhel+epel-$releasever-x86_64/
type=rpm-md
skip_if_unavailable=True
gpgcheck=1
gpgkey=https://download.copr.fedorainfracloud.org/results/mlampe/xrdp/pubkey.gpg
repo_gpgcheck=0
enabled=1
enabled_metadata=1
`

// ensureXrdpInstalled:headless 镜像不带 xrdp,enable-vnc 先装上。
//
// 三步:① 开 EPEL(失败不致命,EPEL10 未收录 xrdp 是常态)→ 试装 xrdp;
// ② 不中则手写 COPR repo 文件 → makecache → 再试;③ 仍不中则报错。
// xrdp-selinux 只在 xrdp 本体装上后尝试(装不上不阻断)。
//
// 代价要说清楚:装包要出网。若机器正是**因为断网**而砖的,VNC 救砖会跟着失败;
// 那时只有串口控制台与 sshd 两条路 —— 这也是调用方要在失败时提示的。
func ensureXrdpInstalled(stderr io.Writer) error {
	// ① EPEL
	if _, err := execOut("dnf", "install", "-y", "epel-release"); err != nil {
		fmt.Fprintln(stderr, "rescue: EPEL 启用失败(EL10 未收录 xrdp,属预期),继续走 COPR")
	}
	if _, err := execOut("dnf", "makecache"); err != nil {
		fmt.Fprintln(stderr, "rescue: dnf makecache 失败,继续")
	}
	if _, err := execOut("dnf", "install", "-y", "xrdp"); err == nil {
		fmt.Fprintln(stderr, "rescue: xrdp 已从 EPEL 装上")
		installXrdpSelinux(stderr)
		return nil
	}
	fmt.Fprintln(stderr, "rescue: EPEL 无 xrdp,改走 COPR mlampe/xrdp")
	// ② COPR
	if err := os.WriteFile(coprRepoPath, []byte(coprRepoBody), 0o644); err != nil {
		return fmt.Errorf("写 COPR repo 文件 %s 失败: %w", coprRepoPath, err)
	}
	if _, err := execOut("dnf", "makecache"); err != nil {
		return fmt.Errorf("dnf makecache 失败(COPR repo 刚写入,可能被拒): %w", err)
	}
	if _, err := execOut("dnf", "install", "-y", "xrdp"); err != nil {
		return fmt.Errorf("EPEL 与 COPR 均未提供 xrdp: %w", err)
	}
	fmt.Fprintln(stderr, "rescue: xrdp 已从 COPR 装上")
	installXrdpSelinux(stderr)
	return nil
}

// installXrdpSelinux:可选的 SELinux 策略包,装不上不阻断(无 SELinux 的环境
// 本来就不需要它)。
func installXrdpSelinux(stderr io.Writer) {
	if _, err := execOut("dnf", "install", "-y", "xrdp-selinux"); err != nil {
		fmt.Fprintln(stderr, "rescue: WARN xrdp-selinux 未装(非致命)")
	}
}

// sesmanIniPath 是 xrdp 会话管理配置。
var sesmanIniPath = "/etc/xrdp/sesman.ini"

// hardenSesmanDepth:在 [Xvnc] 段后补 `param=-depth` / `param=24`。
//
// 为什么:TigerVNC 1.15 的 Xvnc 不支持 depth 32,而 RDP 客户端默认 32bpp,会被
// 拼进 Xvnc 命令行 → Xvnc 启动即退出 → waitforx 超时 → "X server failed to
// start" → VNC 黑屏/秒断。77-xrdp.sh 构建期已经踩过这个坑并写死,按需安装
// 路径必须补上同一处理,否则 rescue 开出来的 VNC 是黑的。
//
// 幂等:[Xvnc] 段里已有 -depth 就原样返回。
func hardenSesmanDepth(_ io.Writer) error {
	raw, err := os.ReadFile(sesmanIniPath)
	if err != nil {
		return err
	}
	lines := strings.Split(string(raw), "\n")
	out := make([]string, 0, len(lines)+2)
	patched := false
	inXvnc := false
	for _, ln := range lines {
		out = append(out, ln)
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			inXvnc = strings.EqualFold(trimmed, "[Xvnc]")
			continue
		}
		if inXvnc && strings.Contains(ln, "-depth") {
			patched = true // 段内已有 -depth,不再插入
		}
		if inXvnc && !patched && strings.TrimSpace(ln) == "" {
			// 插在 [Xvnc] 后第一个空行之前(即紧随标题行)
			out = append(out[:len(out)-1], "param=-depth", "param=24", ln)
			patched = true
		}
	}
	if !patched {
		return fmt.Errorf("sesman.ini 未找到 [Xvnc] 段或段内已有 -depth,未改动")
	}
	return os.WriteFile(sesmanIniPath, []byte(strings.Join(out, "\n")), 0o644)
}

// xrdpBinPath 是 xrdp 包安装的主程序。判断“装没装”只看它在不在 —— 不看 systemctl
// 的 LoadState(理由见 rescueEnableVnc 开头的注释)。
var xrdpBinPath = "/usr/sbin/xrdp"

// xrdpInstalled:报 xrdp 包是否真装了。纯 stat,不 spawn 进程,可测。
func xrdpInstalled() bool {
	_, err := os.Stat(xrdpBinPath)
	return err == nil
}

// unitMissing:systemctl 报“unit 不存在”的文案判定。systemctl 的措辞随版本
// 变过,这里只认三种高频写法,宁可漏判(走报错分支)也不误判(吃掉真错误)。
func unitMissing(out string) bool {
	for _, s := range []string{
		"does not exist",
		"could not be found",
		"not loaded",
	} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

// execOut 是本文件所有外部命令(dnf / systemctl)的唯一出口。返回合并后的
// stdout+stderr —— 调用方靠它判定错误语义(如 systemctl 的 unit 不存在)。
//
// ★ 调用点契约(安全边界):`name` 在**全部**调用点均为字面量("dnf" / "systemctl" /
// "firewall-cmd"),args 同样全为常量。本文件不接收任何来自 flags / 环境 / 网络的
// 字符串,故 exec.Command 无注入面。若日后新增调用点引入变量拼接,必须先把
// name 收敛回白名单常量 —— 这条是该文件唯一的 exec 出口,审这里是够的。
//
// 测试通过替换这个包级变量,在**没装 systemctl / dnf 的 CI 上**驱动
// enable/disable 全流程,不必真机。
var execOut = func(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	return string(out), err
}

// runSystemctl:exec 一条 systemctl 命令,失败时把 stderr 合并回传。失败语义:
// exitRuntime。
func runSystemctl(stderr io.Writer, args ...string) error {
	out, err := execOut("systemctl", args...)
	if err != nil {
		fmt.Fprintf(stderr, "systemctl %s: %v\n%s\n", strings.Join(args, " "), err, out)
		return err
	}
	return nil
}

// openFirewallPort:试 firewall-cmd 在线 + firewall-offline-cmd(后者在 initramfs
// / chroot 时用)。都不可用 → 返回 sentinel 错误由调用方 WARN 跳过。
func openFirewallPort(stderr io.Writer, spec string) error {
	if err := tryFirewallCmd(stderr, "--add-port="+spec); err == nil {
		return nil
	}
	if err := tryFirewallCmd(stderr, "--add-port="+spec, "--runtime-to-permanent"); err == nil {
		return nil
	}
	if _, err := execOut("firewall-offline-cmd", "--add-port="+spec); err == nil {
		return nil
	}
	return fmt.Errorf("firewall-cmd / firewall-offline-cmd 不可用或失败")
}

// closeFirewallPort:对称撤端口。
func closeFirewallPort(stderr io.Writer, spec string) error {
	if err := tryFirewallCmd(stderr, "--remove-port="+spec); err == nil {
		return nil
	}
	if err := tryFirewallCmd(stderr, "--remove-port="+spec, "--runtime-to-permanent"); err == nil {
		return nil
	}
	if _, err := execOut("firewall-offline-cmd", "--remove-port="+spec); err == nil {
		return nil
	}
	return fmt.Errorf("firewall-cmd / firewall-offline-cmd 不可用或失败")
}

// tryFirewallCmd:exec firewall-cmd 一条命令。失败时 stderr 仅 WARN(避免
// enable/disable 流程因 firewall 缺位而噪音爆炸)。命令不存在 → 返回 sentinel。
func tryFirewallCmd(stderr io.Writer, args ...string) error {
	// 走 execOut 这一个出口:命令不存在时 exec.Command 自身报 exec.ErrNotFound,
	// 与原先的 LookPath 预检等价,但让测试能整条替换掉(firewall 是唯一会在
	// 开发机上真的改动系统状态的调用,不能漏在 mock 之外)。
	out, err := execOut("firewall-cmd", args...)
	if err != nil {
		fmt.Fprintf(stderr, "firewall-cmd %s: %v\n%s\n", strings.Join(args, " "), err, out)
		return err
	}
	return nil
}
