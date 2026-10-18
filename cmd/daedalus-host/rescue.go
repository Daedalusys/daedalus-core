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
	XrdpUnitState string // "masked" | "disabled" | "enabled" | "running" | "unknown"
	Port3389Open  bool   // 3389/tcp 是否监听(本机 127.0.0.1)
	IsHeadless    bool   // /etc/os-release 含 VARIANT=headless
}

// osReleasePath 是 os-release 文件的标准路径。constant 方便测试或未来重定向。
const osReleasePath = "/etc/os-release"

// startwmPath 是 xrdp 默认 startwm.sh 路径。enable-vnc 必须覆写它。
const startwmPath = "/etc/xrdp/startwm.sh"

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

// probeXrdpUnitState:`systemctl show xrdp -p LoadState -p SubState -p ActiveState`
// 三件套合成一个最终状态串。systemctl 缺失 / xrdp unit 不存在 / 失败 →
// ("unknown", false),由调用方按字段非空兜底。
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
	load := fields["LoadState"] // masked | not-found | loaded | …
	sub := fields["SubState"]   // running | dead | exited | failed | …
	act := fields["ActiveState"] // active | inactive | failed | …
	switch {
	case load == "masked":
		return "masked", true
	case act == "active" && sub == "running":
		return "running", true
	case act == "active":
		return "enabled", true
	case load == "not-found":
		return "disabled", true
	default:
		if load != "" {
			return load, true
		}
		return "unknown", false
	}
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

// rescueEnableVnc:把 xrdp 从 masked → running;落 startwm.sh 兜底 stub,
// 写 firewall 规则,报最终状态。任一步失败 stderr 报原因并返回 exitRuntime;
// 全部成功 → exitOK。
func rescueEnableVnc(stdout, stderr io.Writer, _ rescueState) int {
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
// xterm stub(无害且阻止 KDE-shaped 未来回归)。
func rescueDisableVnc(stdout, stderr io.Writer, _ rescueState) int {
	if err := runSystemctl(stderr, "disable", "--now", "xrdp", "xrdp-sesman"); err != nil {
		fmt.Fprintln(stderr, "rescue: systemctl disable --now 失败:", err)
		return exitRuntime
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

// rescueStatus:打印 xrdp unit state + 3389 端口状态。结构固定:多行,
// 首行 "xrdp: <state>",第二行 "3389/tcp: open|closed"。
func rescueStatus(stdout, _ io.Writer, state rescueState) int {
	port := "closed"
	if state.Port3389Open {
		port = "open"
	}
	fmt.Fprintf(stdout, "xrdp: %s\n", state.XrdpUnitState)
	fmt.Fprintf(stdout, "3389/tcp: %s\n", port)
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

// runSystemctl:exec 一条 systemctl 命令,捕获 stderr 合并回传。失败语义:
// exitRuntime。
func runSystemctl(stderr io.Writer, args ...string) error {
	cmd := exec.Command("systemctl", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(stderr, "systemctl %s: %v\n%s\n", strings.Join(args, " "), err, string(out))
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
	if _, lookErr := exec.LookPath("firewall-offline-cmd"); lookErr == nil {
		if err := exec.Command("firewall-offline-cmd", "--add-port="+spec).Run(); err == nil {
			return nil
		}
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
	if _, lookErr := exec.LookPath("firewall-offline-cmd"); lookErr == nil {
		if err := exec.Command("firewall-offline-cmd", "--remove-port="+spec).Run(); err == nil {
			return nil
		}
	}
	return fmt.Errorf("firewall-cmd / firewall-offline-cmd 不可用或失败")
}

// tryFirewallCmd:exec firewall-cmd 一条命令。失败时 stderr 仅 WARN(避免
// enable/disable 流程因 firewall 缺位而噪音爆炸)。命令不存在 → 返回 sentinel。
func tryFirewallCmd(stderr io.Writer, args ...string) error {
	path, err := exec.LookPath("firewall-cmd")
	if err != nil {
		return err
	}
	cmd := exec.Command(path, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		fmt.Fprintf(stderr, "firewall-cmd %s: %v\n%s\n", strings.Join(args, " "), err, string(out))
		return err
	}
	return nil
}
