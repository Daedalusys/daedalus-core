#!/usr/bin/env bash
# =============================================================================
# cd-verify.sh —— Daedalus CD 验证一键驱动(在开发机上跑)
# =============================================================================
# 干什么
#   把「改好的 CD 验证管线」推上去、开 PR、盯 CI、装自托管 runner、触发真 KVM
#   引导验证、回收证据 —— 一条命令串起来,每一步也可单跑。
#
# 背景
#   P0(容器行为探针)跑在托管 runner 上:推 feat/* 分支即触发 build-image job,
#   其中新增的 "Headless deployment probes" 步会真起容器验 A–F。
#   P1(KVM 真引导)跑在自托管 runner 上:需要靶机先装好 runner(见 runner 子命令),
#   然后出一版镜像 + qcow2,再在真 KVM 上等串口 login。
#
# 用法
#   scripts/cd-verify.sh preflight      # 只体检(gh 登录/git 状态/分支)
#   scripts/cd-verify.sh push           # 推分支 + 开 PR
#   scripts/cd-verify.sh watch [run-id] # 盯 CI run,汇总各 job 结论(有 job 红则退出 1)
#   scripts/cd-verify.sh runner [--dry] # 在靶机上装自托管 runner(需交互输 sudo 密码;--dry 只体检)
#   scripts/cd-verify.sh p1             # 触发出货档 workflow_dispatch(真 KVM 引导)
#   scripts/cd-verify.sh evidence       # 下载最近一次 P1 证据 artifact
#   scripts/cd-verify.sh all            # preflight → push → watch → (视情况)runner/p1
#
# 环境变量
#   CD_REPO        目标仓(默认 Daedalusys/daedalus-core)
#   CD_BRANCH      分支(默认 feat/cd-verify-pipeline)
#   CD_TARGET      靶机 ssh 别名(默认 Masterwet_Server)
#   CD_REF         p1 的 ref 覆盖(默认:分支已并入 main 则 main,否则按需)
#   CD_ALLOW_FEATURE_PROMOTE  设为 1 允许用未合并分支跑 P1(会把该分支镜像晋升
#                             :latest/:upstream,慎用)
#
# 退出码
#   0 成功 / 1 失败(CI 有 job 红、步骤出错) / 2 前置不满足
# =============================================================================
set -euo pipefail

REPO="${CD_REPO:-Daedalusys/daedalus-core}"
BRANCH="${CD_BRANCH:-feat/cd-verify-pipeline}"
TARGET="${CD_TARGET:-Masterwet_Server}"
WORKFLOW="build.yml"
RUNNER_LABEL="daedalus-kvm"

say()  { printf '\n\033[1;36m== %s ==\033[0m\n' "$*"; }
info() { printf '   %s\n' "$*"; }
ok()   { printf '\033[1;32m   OK %s\033[0m\n' "$*"; }
warn() { printf '\033[1;33m   !! %s\033[0m\n' "$*"; }
bad()  { printf '\033[1;31m   XX %s\033[0m\n' "$*" >&2; }
die()  { bad "$*"; exit 1; }

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"   # daedalus-core 仓根
POLL_SECS="${POLL_SECS:-20}"

# ------------------------------------------------------------------ helpers ---
assert_auth() {
    gh auth status >/dev/null 2>&1 || die "gh 未登录(先 gh auth login)"
}

job_table() {  # job_table <run-id> —— 打印各 job 名字/结论
    gh run view "$1" --repo "$REPO" --json jobs \
        --jq '.jobs[] | "   \(.conclusion // .status)\t\(.name)"' | sed 's/\t/  /'
}

wait_run() {  # wait_run <run-id> —— 轮询到 completed,把最终 conclusion 打到 stdout
    # 进度一律走 stderr:本函数的 stdout 会被调用方 $( ) 捕获成结论,不能混入噪声。
    local id="$1" line status conclusion
    while :; do
        line="$(gh run view "$id" --repo "$REPO" --json status,conclusion \
                --jq '.status + " " + (.conclusion // "")' 2>/dev/null || true)"
        status="${line%% *}"
        conclusion="${line#* }"
        [ "$conclusion" = "$line" ] && conclusion=""
        case "$status" in
            completed) printf '%s' "${conclusion:-unknown}"; return 0 ;;
        esac
        printf '   ... %s\n' "${status:-未知}" >&2
        sleep "$POLL_SECS"
    done
}

latest_run_id() {  # latest_run_id [branch] —— 最近一次 run id(无则空)
    local b="${1:-$BRANCH}"
    gh run list --repo "$REPO" --workflow "$WORKFLOW" --branch "$b" --limit 1 \
        --json databaseId --jq '(.[0] // {}).databaseId // empty' 2>/dev/null || true
}

# ---------------------------------------------------------------- preflight ---
cmd_preflight() {
    say "preflight"
    assert_auth; ok "gh 已登录"
    git -C "$ROOT" rev-parse --git-dir >/dev/null 2>&1 || die "$ROOT 不是 git 仓"
    local cur; cur="$(git -C "$ROOT" rev-parse --abbrev-ref HEAD)"
    ok "当前分支: $cur"
    [ "$cur" = "$BRANCH" ] || warn "当前不在 $BRANCH(后续 push 前请切过去)"
    if [ -n "$(git -C "$ROOT" status --porcelain)" ]; then
        warn "工作树有未提交改动:"; git -C "$ROOT" status --porcelain | sed 's/^/      /'
    else
        ok "工作树干净"
    fi
    git -C "$ROOT" remote get-url origin >/dev/null 2>&1 || die "缺 origin remote"
    ok "origin: $(git -C "$ROOT" remote get-url origin)"
    echo
    info "分支领先 main 的提交:"
    git -C "$ROOT" log --oneline main.."$BRANCH" 2>/dev/null | sed 's/^/      /' || true
}

# --------------------------------------------------------------------- push ---
cmd_push() {
    say "push 分支 + 开 PR"
    assert_auth
    git -C "$ROOT" rev-parse --verify "$BRANCH" >/dev/null 2>&1 || die "本地没有分支 $BRANCH"
    info "推送 $BRANCH → origin"
    git -C "$ROOT" push -u origin "$BRANCH"
    ok "已推送"

    local url
    url="$(gh pr list --repo "$REPO" --head "$BRANCH" --json url --jq '.[0].url // empty')"
    if [ -n "$url" ]; then
        ok "PR 已存在: $url"
    else
        info "创建 PR"
        url="$(gh pr create --repo "$REPO" --base main --head "$BRANCH" \
            --title "feat(ci): CD 验证管线 —— P0 容器探针 + P1 KVM 真引导门禁" \
            --body "$(cat <<'BODY'
## 动机

整条产线此前**只验「镜像构建成功」,从不验「镜像起不起来」**:九个 workflow、全部
托管 runner,零自托管 runner、零 `bootc switch/install`、零 VM 引导。

本 PR 把仓库里已有的两个实机 harness 接成 CI 门禁:

- **P0 — 容器行为探针**(托管 runner,零新硬件):在 `build-image` job 内真起
  systemd 容器跑 A1–F1 探针(单元产物/生命周期/drift/ownership/审计链/smoke)。
- **P1 — KVM 真引导**(自托管 runner,标签 `daedalus-kvm`):把 `build-qcow2` 出的
  qcow2 在真 KVM 上引导,串口必须出现 `login:` 提示符;证据(串口日志/lsblk diff)
  回传 artifact。

## 改动

- `tests/deploy/headless-test.sh`:新增 local 执行模式(供托管 runner 用);去掉
  `--network=host`(会抢宿主 22 端口、动宿主网卡);补 **FAIL 退出码契约** ——
  此前恒 `exit 0` 是假绿。
- `.github/workflows/build.yml`:`build-image` 内新增 P0 步(x86-64-v3 前置,**响亮
  失败**,不静默跳过);文件末尾新增 `deploy-verify-kvm` job(只对出货档触发,
  ⛔ **不对 PR / feat\* 开放** —— 自托管 runner 会执行构建产物)。
- `scripts/cd-verify.sh` / `scripts/cd-runner-setup.sh`:CD 一键驱动与靶机 runner
  安装脚本。

## 验证

- `bash -n tests/deploy/headless-test.sh` ✅
- `tests/deploy/headless-test.sh --selftest` → `RESULT: SELFTEST PASS` ✅
- `tests/ci/workflow-hygiene.sh`(真四仓门禁)→ `OK` ✅
- P0 全绿路径只能在 CI 里跑(本机无 AVX2,起不了目标镜像),本 PR 的 CI 即其验证。
- P1 需先在靶机装好自托管 runner,再 `workflow_dispatch`(PUSH=true)验证。

## 未做 / 边界

- P1 需要靶机 runner(人工装,`scripts/cd-runner-setup.sh` 是安装脚本)。
- P2(bootc `switch/upgrade/rollback`、ISO 接入 CI、qcow2 提为 Release 资产)不在本 PR。
- 不自动合并 —— 开给人审。
BODY
)")"
        ok "PR created: $url"
    fi
    echo "$url"
}

# -------------------------------------------------------------------- watch ---
cmd_watch() {
    say "watch CI($BRANCH)"
    assert_auth
    local id="${1:-}"
    if [ -z "$id" ]; then
        id="$(latest_run_id "$BRANCH")"
        [ -n "$id" ] || die "找不到 $BRANCH 的 run(先 push?)"
    fi
    info "run id: $id"
    info "url: https://github.com/$REPO/actions/runs/$id"
    local conclusion; conclusion="$(wait_run "$id")"
    say "job 结论"
    job_table "$id"
    echo
    if [ "$conclusion" = "success" ]; then ok "run 结论: success"; return 0; fi
    bad "run 结论: $conclusion"
    info "失败 job 日志尾部:"
    gh run view "$id" --repo "$REPO" --log-failed 2>/dev/null | tail -40 | sed 's/^/      /' || true
    return 1
}

# ------------------------------------------------------------------- runner ---
cmd_runner() {
    local dry=0
    [ "${1:-}" = "--dry" ] && dry=1
    say "在靶机 $TARGET 上装自托管 runner${dry:+（干跑:只体检宿主能力）}"
    assert_auth

    info "传安装脚本到靶机 /tmp/"
    scp -q "$ROOT/scripts/cd-runner-setup.sh" "$TARGET:/tmp/cd-runner-setup.sh" \
        || die "scp 失败(靶机可达?ssh 免密?)"

    if [ "$dry" = 1 ]; then
        # 干跑:不需要 token、不需要 sudo,先把宿主能力验一遍
        ssh "$TARGET" 'bash /tmp/cd-runner-setup.sh --preflight-only' \
            || die "靶机体检未通过(见上方输出)"
        ok "靶机体检通过;去掉 --dry 重跑即可正式安装"
        return 0
    fi

    info "取一次性注册 token"
    local token
    token="$(gh api -X POST "repos/$REPO/actions/runners/registration-token" --jq .token)"
    [ -n "$token" ] || die "取 token 失败"

    # token 走文件而非命令行参数,避免出现在远端 ps 里
    printf '%s' "$token" | ssh "$TARGET" 'umask 077; cat > /tmp/.daedalus-runner-token' \
        || die "写 token 文件失败"

    info "在靶机上执行(会交互提示 sudo 密码)"
    ssh -t "$TARGET" 'bash /tmp/cd-runner-setup.sh --token-file /tmp/.daedalus-runner-token' \
        || die "靶机安装脚本失败"

    say "确认 runner 已注册"
    gh api "repos/$REPO/actions/runners" \
        --jq '.runners[] | "   \(.name)  \(.status)  \(.labels|map(.name)|join(","))"'
    ok "runner 安装流程结束(上面应出现带 $RUNNER_LABEL 标签的 online runner)"
}

# ----------------------------------------------------------------------- p1 ---
cmd_p1() {
    say "P1 · 触发真 KVM 引导验证"
    assert_auth
    git -C "$ROOT" fetch origin --quiet || true

    local ref="${CD_REF:-}"
    if [ -z "$ref" ]; then
        if git -C "$ROOT" merge-base --is-ancestor "$BRANCH" origin/main 2>/dev/null; then
            ref="main"; ok "$BRANCH 已并入 origin/main,用 ref=main"
        else
            warn "$BRANCH 尚未并入 main —— deploy-verify-kvm job 还不在 main 的 workflow 里"
            if [ "${CD_ALLOW_FEATURE_PROMOTE:-0}" = "1" ]; then
                ref="$BRANCH"
                warn "CD_ALLOW_FEATURE_PROMOTE=1:用未合并分支跑,会把该分支镜像晋升为 :latest/:upstream"
            else
                cat <<EOF
   请二选一:
     1) 先合并 PR 到 main,再重跑:$(basename "$0") p1
     2) 明知副作用(镜像 :latest 会指向未合并分支)时:
        CD_ALLOW_FEATURE_PROMOTE=1 $(basename "$0") p1
EOF
                return 2
            fi
        fi
    fi

    # runner 是否在线?
    local online
    online="$(gh api "repos/$REPO/actions/runners" \
        --jq "[.runners[] | select(.labels[].name==\"$RUNNER_LABEL\")] | length" 2>/dev/null || echo 0)"
    if [ "${online:-0}" = "0" ]; then
        warn "没有带 $RUNNER_LABEL 标签的在线 runner —— deploy-verify-kvm 会一直排队"
        warn "先跑:$(basename "$0") runner"
        return 2
    fi
    ok "在线 runner 数: $online"

    info "dispatch: ref=$ref VARIANT=headless PUSH=true"
    gh workflow run "$WORKFLOW" --repo "$REPO" --ref "$ref" \
        -f VARIANT=headless -f PUSH=true || die "dispatch 失败"
    ok "已触发,等待 run 出现"
    local id="" i
    for i in $(seq 1 30); do
        id="$(latest_run_id "$ref")"
        [ -n "$id" ] && break
        sleep 5
    done
    [ -n "$id" ] || die "没等到新 run"
    info "run id: $id"
    local conclusion; conclusion="$(wait_run "$id")"
    say "job 结论"
    job_table "$id"
    echo
    [ "$conclusion" = "success" ] && ok "run: success" || bad "run: $conclusion"
    info "下一个:$(basename "$0") evidence"
    [ "$conclusion" = "success" ] || return 1
}

# ----------------------------------------------------------------- evidence ---
cmd_evidence() {
    say "下载 P1 证据"
    assert_auth
    local id="" dest
    id="$(latest_run_id "main")"
    [ -n "$id" ] || id="$(latest_run_id "$BRANCH")"
    [ -n "$id" ] || die "找不到 run"
    dest="$ROOT/../cd-evidence/$id"
    mkdir -p "$dest"
    info "从 run $id 下载 deploy-verify-kvm-evidence → $dest"
    gh run download "$id" --repo "$REPO" -n deploy-verify-kvm-evidence -D "$dest" \
        || { bad "该 run 没有证据 artifact(可能 P1 未跑或前置失败)"; return 1; }
    ok "证据已下载:"
    find "$dest" -type f | sed 's/^/      /'
    info "关键文件:serial-console.log(串口输出)、bootc-verify.log、lsblk.diff"
}

# ---------------------------------------------------------------------- all ---
cmd_all() {
    cmd_preflight
    cmd_push
    say "盯 P0(托管 runner 的容器探针)"
    if cmd_watch; then
        ok "P0 通过 —— 镜像在真容器里能起、探针全绿"
    else
        bad "P0 未通过 —— 先修 CI,再谈 P1"
        return 1
    fi
    echo
    say "下一步:P1"
    if [ "$(gh api "repos/$REPO/actions/runners" --jq "[.runners[] | select(.labels[].name==\"$RUNNER_LABEL\")] | length" 2>/dev/null || echo 0)" != "0" ]; then
        info "检测到在线 runner,继续 p1"
        cmd_p1 && cmd_evidence || true
    else
        cat <<EOF
   靶机上还没装自托管 runner。装好后重跑:

     $(basename "$0") runner      # 在 $TARGET 上装(交互输 sudo 密码)
     $(basename "$0") p1          # 触发真 KVM 引导(合并 PR 后)
     $(basename "$0") evidence    # 回收串口日志等证据
EOF
    fi
}

# -------------------------------------------------------------------- entry ---
case "${1:-}" in
    preflight) shift; cmd_preflight "$@" ;;
    push)      shift; cmd_push "$@" ;;
    watch)     shift; cmd_watch "$@" ;;
    runner)    shift; cmd_runner "$@" ;;
    p1)        shift; cmd_p1 "$@" ;;
    evidence)  shift; cmd_evidence "$@" ;;
    all)       shift; cmd_all "$@" ;;
    ""|-h|--help) sed -n '2,45p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//' ;;
    *) echo "未知子命令: $1(用 --help)" >&2; exit 2 ;;
esac
