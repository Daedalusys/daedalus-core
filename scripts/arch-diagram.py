#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""生成 Daedalus 完整架构图(单张 SVG)。

可复现:纯标准库,零第三方依赖;同样代码必得同样字节输出。
用法:
    python3 scripts/arch-diagram.py                 # 写 docs/architecture.svg
    python3 scripts/arch-diagram.py -o /tmp/a.svg   # 指定路径
    python3 scripts/arch-diagram.py --png           # 同时出 PNG(需 rsvg-convert)
"""

from __future__ import annotations

import argparse
import html
import shutil
import subprocess
import sys
from pathlib import Path

# ── 画布与配色 ──────────────────────────────────────────────
W, H = 1280, 920
BG = "#ffffff"
FONT = "Noto Sans CJK SC, Noto Sans SC, sans-serif"

C_USER = "#e8eaf6"
C_COPILOT = "#e3f2fd"
C_CAP = "#e8f5e9"
C_SPEC = "#fff8e1"
C_TX = "#ffebee"
C_OS = "#eceff1"
C_OBS = "#e0f7fa"
C_CROSS = "#f3e5f5"
C_RUNTIME = "#efebe9"
C_REPO = "#f5f5f5"

BORDER = "#546e7a"
TEXT = "#212121"
TEXT_DIM = "#546e7a"
ARROW = "#37474f"


def esc(s: str) -> str:
    return html.escape(s, quote=True)


class Svg:
    def __init__(self, w: int, h: int) -> None:
        self.w, self.h = w, h
        self.parts: list[str] = []

    def rect(self, x, y, w, h, fill, stroke=BORDER, rx=8, sw=1.5, opacity=1.0) -> None:
        self.parts.append(
            f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="{rx}" '
            f'fill="{fill}" stroke="{stroke}" stroke-width="{sw}" opacity="{opacity}"/>'
        )

    def label_box(self, x, y, w, h, fill, title, lines,
                  title_size=15, body_size=12, stroke=BORDER) -> None:
        self.rect(x, y, w, h, fill, stroke=stroke)
        if title:
            self.text(x + w / 2, y + 22, title, size=title_size, bold=True, anchor="middle")
        for i, line in enumerate(lines):
            self.text(x + w / 2, y + (44 if title else 22) + i * 17,
                      line, size=body_size, anchor="middle", fill=TEXT_DIM)

    def text(self, x, y, s, size=13, bold=False, anchor="start", fill=TEXT) -> None:
        weight = ' font-weight="bold"' if bold else ""
        self.parts.append(
            f'<text x="{x}" y="{y}" font-family="{esc(FONT)}" font-size="{size}"'
            f'{weight} fill="{fill}" text-anchor="{anchor}">{esc(s)}</text>'
        )

    def line(self, x1, y1, x2, y2, stroke=ARROW, sw=1.8, dashed=False) -> None:
        dash = ' stroke-dasharray="6,4"' if dashed else ""
        self.parts.append(
            f'<line x1="{x1}" y1="{y1}" x2="{x2}" y2="{y2}" '
            f'stroke="{stroke}" stroke-width="{sw}"{dash} marker-end="url(#arrow)"/>'
        )

    def polyline(self, pts, stroke=ARROW, sw=1.8, dashed=False) -> None:
        dash = ' stroke-dasharray="6,4"' if dashed else ""
        pt = " ".join(f"{x},{y}" for x, y in pts)
        self.parts.append(
            f'<polyline points="{pt}" fill="none" stroke="{stroke}" '
            f'stroke-width="{sw}"{dash} marker-end="url(#arrow)"/>'
        )

    def group_title(self, x, y, w, h, title) -> None:
        self.parts.append(
            f'<rect x="{x}" y="{y}" width="{w}" height="{h}" rx="10" fill="none" '
            f'stroke="{BORDER}" stroke-width="1.2" stroke-dasharray="4,3" opacity="0.55"/>'
        )
        self.text(x + 10, y + 18, title, size=12, bold=True, fill=TEXT_DIM)

    def render(self) -> str:
        defs = f"""<defs>
  <marker id="arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
    <path d="M 0 0 L 10 5 L 0 10 z" fill="{ARROW}"/>
  </marker>
</defs>"""
        body = "\n  ".join(self.parts)
        return (
            f'<svg xmlns="http://www.w3.org/2000/svg" width="{self.w}" height="{self.h}" '
            f'viewBox="0 0 {self.w} {self.h}">\n{defs}\n'
            f'  <rect width="100%" height="100%" fill="{BG}"/>\n  {body}\n</svg>\n'
        )


def build() -> str:
    s = Svg(W, H)

    # ── 标题 ──
    s.text(W / 2, 30, "Daedalus 完整架构图", size=22, bold=True, anchor="middle")
    s.text(W / 2, 52,
           "主链:声明 → 事务 → 真实系统 → 观测;横切:策略 + 审计;下层:运行时进程与三仓",
           size=12, anchor="middle", fill=TEXT_DIM)

    # ── 用户 ──
    s.label_box(430, 68, 420, 50, C_USER, "用户 / AI agent",
                ["自然语言意图 · MCP 工具调用"], title_size=16)

    # ── copilot | capability ──
    s.label_box(
        70, 150, 470, 105, C_COPILOT, "copilot 命令顾问  (daedalus.copilot)",
        [
            "本地静态分级 L0/L1/L2,LLM 不自评风险",
            "L0 → y/n 后进入 shell 沙箱 或 事务五步",
            "L1/L2 → 仅展示 + 手动执行提示,不进执行通道",
        ],
        title_size=14,
    )
    s.label_box(
        620, 150, 590, 105, C_CAP, "capability 服务器 × 7  (Go 静态二进制 · 只读)",
        [
            "fs · shell · pkg · sysinfo · service · blueprint · dupe",
            "service.query / service.list 等 MCP 工具",
            "查询严格只读;写状态一律经 daedalus-tx",
        ],
        title_size=14,
    )
    s.polyline([(640, 118), (640, 134), (305, 134), (305, 150)])
    s.polyline([(640, 118), (640, 150)])

    # ── 三面核心 ──
    gy, gh = 285, 250
    s.group_title(55, gy, 1170, gh, "三面架构(核心数据流)")

    s.label_box(
        85, gy + 40, 310, 145, C_SPEC, "声明面 Spec",
        [
            "系统应该是什么样",
            "Resource = kind + name + desired_state",
            "标准动词:set / delete(→absent)",
            "改的是资源声明,不是裸命令字符串",
        ],
        title_size=15,
    )
    s.label_box(
        455, gy + 40, 330, 145, C_TX, "执行面 Tx  (daedalus-tx)",
        [
            "怎么改、怎么恢复",
            "begin → propose → preview → y/n → apply",
            "事务日志 + before/after 快照 + 回滚计划",
            "网关:enabled_kinds 放行 service / package",
        ],
        title_size=15,
    )
    s.label_box(
        845, gy + 40, 350, 145, C_OS, "真实系统  (OS 即事实源)",
        [
            "systemd 单元 · dnf/rpm · 文件系统",
            "无 etcd 式集中期望态库",
            "journal 存期望,OS 存现实,事务是桥",
        ],
        title_size=15,
    )
    s.line(395, gy + 112, 453, gy + 112)
    s.text(424, gy + 102, "一个事务", size=11, anchor="middle", fill=TEXT_DIM)
    s.line(785, gy + 112, 843, gy + 112)
    s.text(814, gy + 102, "副作用", size=11, anchor="middle", fill=TEXT_DIM)

    # 观测面(核心区下缘)
    s.label_box(
        300, gy + 200, 620, 62, C_OBS, "观测面 Observe  (只读对账)",
        ["query / list · state.jsonl 最新值缓存 · 给 diff、审计、下一步决策供料"],
        title_size=14,
    )
    s.polyline([(1020, gy + 185), (1020, gy + 231), (920, gy + 231)])
    s.polyline([(300, gy + 231), (240, gy + 231), (240, gy + 185)], dashed=True)
    s.text(270, gy + 221, "回读对账", size=10, anchor="middle", fill=TEXT_DIM)

    # 入口连线:copilot → 声明面(左侧直下)
    s.polyline([(305, 255), (305, gy + 40)])
    # capability → 观测面:沿右缘绕开真实系统,再左折进观测面
    s.polyline([(1180, 255), (1245, 255), (1245, gy + 231), (920, gy + 231)])

    # ── 横切 ──
    cy = gy + gh + 18
    s.group_title(55, cy, 1170, 78, "两道横切(作用于每一面、每一条通道)")
    s.label_box(
        90, cy + 24, 530, 46, C_CROSS, "策略面  policy.toml",
        ["缺省 fail-closed · enabled_kinds 网关 · 15 命令白名单 · 凭证隔离"],
        title_size=13,
    )
    s.label_box(
        660, cy + 24, 530, 46, C_CROSS, "证据面  audit.jsonl 哈希链",
        ["唯一写入口 daedalus-audit · begin/apply/rollback 盖 TxID+TxStep"],
        title_size=13,
    )

    # ── 运行时 + 三仓 ──
    ry = cy + 96
    s.group_title(55, ry, 560, 155, "运行时进程(零 spawn · systemd 沙箱)")
    s.label_box(
        80, ry + 28, 510, 115, C_RUNTIME, "启动链",
        [
            "daedalus-host:list/inspect/verify — 只打印启动命令,绝不 spawn",
            "systemd 单元:DynamicUser + Landlock + seccomp + LoadCredential",
            "76 脚本构建期渲染 ExecStart → 服务器由 systemd 执行",
            "一切落盘审计走 daedalus-audit CLI",
        ],
        title_size=13,
    )

    s.group_title(645, ry, 580, 155, "三仓拓扑与制品流(平级 clone)")
    s.label_box(
        670, ry + 28, 175, 100, C_REPO, "daedalus-core",
        ["镜像 + 5 runtime", "copilot 源码", "internal/{controller,tx}"],
        title_size=13,
    )
    s.label_box(
        860, ry + 28, 175, 100, C_REPO, "daedalus-sdk",
        ["对象模型 / 策略", "审计 / 观测缓存", "公开 contract 包"],
        title_size=13,
    )
    s.label_box(
        1050, ry + 28, 175, 100, C_REPO, "daedalus-plugins",
        ["7 个 capability", "插件 monorepo", "zip + sha256 分发"],
        title_size=13,
    )
    s.text(
        925, ry + 148,
        "just plugin-pack / sync / build → bootc 镜像(构建期内建,无运行时联网安装)",
        size=11, anchor="middle", fill=TEXT_DIM,
    )

    # ── 页脚 ──
    s.text(
        W / 2, H - 14,
        "保证附着于通道:走资源+事务通道 → 可回滚、有日志、有哈希链;其余通道在硬边界内存在,平台不背书。",
        size=11, anchor="middle", fill=TEXT_DIM,
    )
    return s.render()


def main() -> int:
    ap = argparse.ArgumentParser(description="生成 Daedalus 单张完整架构图 (SVG)")
    ap.add_argument(
        "-o", "--output",
        default=str(Path(__file__).resolve().parent.parent / "docs" / "architecture.svg"),
        help="SVG 输出路径(默认 docs/architecture.svg)",
    )
    ap.add_argument("--png", action="store_true", help="同时用 rsvg-convert 导出同名 PNG")
    args = ap.parse_args()

    out = Path(args.output)
    out.parent.mkdir(parents=True, exist_ok=True)
    svg = build()
    out.write_text(svg, encoding="utf-8")
    print(f"SVG → {out}  ({out.stat().st_size} bytes)")

    if args.png:
        rsvg = shutil.which("rsvg-convert")
        if not rsvg:
            print("rsvg-convert 未安装,跳过 PNG", file=sys.stderr)
            return 1
        png = out.with_suffix(".png")
        subprocess.run([rsvg, "-o", str(png), str(out)], check=True)
        print(f"PNG → {png}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
