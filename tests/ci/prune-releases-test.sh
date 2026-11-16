#!/usr/bin/env bash
# prune-releases.sh 的行为自检:这个脚本会真删东西,所以每条失败路径都得钉死。
#
# 钉住的不是「删得对不对」这么笼统的事,而是四条安全边界:
#   读不出列表 → 不删(否则一次 API 抖动就让保留策略静默失效);
#   非 sha tag / draft / prerelease → 永不删(语义版本号是人手建的,删了不可逆);
#   最新 N 个 → 永不删;待删数超上限 → 拒跑(配置或解析出错的信号)。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SCRIPT="$ROOT/scripts/prune-releases.sh"

PASS_N=0; FAIL_N=0
st() { # st <期望> <实际> <说明>
    if [ "$1" = "$2" ]; then printf '  PASS  %s\n' "$3"; PASS_N=$((PASS_N + 1))
    else printf '  FAIL  %s (期望 %q,实际 %q)\n' "$3" "$1" "$2"; FAIL_N=$((FAIL_N + 1)); fi
}

printf '== prune-releases-test.sh ==\n'
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# 假 gh:release list 直接吐 $FAKE_RELEASES 的 tsv;release delete 把 tag 记进 $FAKE_DELETED
mkdir -p "$TMP/bin"
cat >"$TMP/bin/gh" <<'FAKE'
#!/usr/bin/env bash
case "$2" in
    list)
        [ "${FAKE_LIST_FAIL:-0}" = 1 ] && { echo "boom" >&2; exit 1; }
        cat "${FAKE_RELEASES:?}";;
    delete)
        [ "${FAKE_DELETE_FAIL:-0}" = 1 ] && exit 1
        printf '%s\n' "$3" >>"${FAKE_DELETED:?}";;
    *) echo "unexpected gh $*" >&2; exit 64;;
esac
FAKE
chmod +x "$TMP/bin/gh"

# 7 个 release:4 个 sha(新的在前)、1 个语义版本号、1 个 draft、1 个 prerelease
cat >"$TMP/releases.tsv" <<'REL'
2026-11-16T12:56:58Z	6bf2485aa943	false	false
2026-11-16T12:22:42Z	d48ff6cedc33	false	false
2026-11-16T12:04:59Z	1ad4c3f20bda	false	false
2026-11-15T11:11:54Z	9d85ee153b48	false	false
2026-11-14T09:00:00Z	v0.1.2	false	false
2026-11-13T09:00:00Z	deadbeef123	true	false
2026-11-12T09:00:00Z	cafebabe456	false	true
REL

run() { # run <期望退出码> <说明> [args...]
    local want="$1" desc="$2"; shift 2
    : >"$TMP/deleted"
    local rc=0
    PATH="$TMP/bin:$PATH" FAKE_RELEASES="$TMP/releases.tsv" FAKE_DELETED="$TMP/deleted" \
        bash "$SCRIPT" --repo Daedalusys/daedalus-core "$@" >"$TMP/out" 2>&1 || rc=$?
    st "$want" "$rc" "$desc"
}

run 2 "--keep 0 → 拒(会删掉当前出货的 Release)" --keep 0
st 0 "$(wc -l <"$TMP/deleted" | tr -d ' ')" "--keep 0 时什么都没删"

run 0 "--keep 2 → 正常" --keep 2
st 2 "$(wc -l <"$TMP/deleted" | tr -d ' ')" "--keep 2:删掉 2 个旧的 sha release"
st 0 "$(grep -c '^6bf2485aa943$' "$TMP/deleted" || true)" "最新的永不删"
st 0 "$(grep -c '^d48ff6cedc33$' "$TMP/deleted" || true)" "第 2 新的永不删"
grep -q '^1ad4c3f20bda$' "$TMP/deleted" && r=0 || r=1; st 0 "$r" "第 3 新的被删"
grep -q '^9d85ee153b48$' "$TMP/deleted" && r=0 || r=1; st 0 "$r" "第 4 新的被删"
st 0 "$(grep -c '^v0.1.2$' "$TMP/deleted" || true)" "语义版本号永不删"
st 0 "$(grep -c '^deadbeef123$' "$TMP/deleted" || true)" "draft 永不删"
st 0 "$(grep -c '^cafebabe456$' "$TMP/deleted" || true)" "prerelease 永不删"

run 0 "--dry-run → 什么都不删" --keep 2 --dry-run
st 0 "$(wc -l <"$TMP/deleted" | tr -d ' ')" "--dry-run 没删任何东西"
grep -q 'WOULD DELETE' "$TMP/out" && r=0 || r=1; st 0 "$r" "--dry-run 打印了将删列表"

# 读不出列表 → 必须非 0 且不删(把 API 失败当成「没有可删的」是最坏的退化)
: >"$TMP/deleted"; rc=0
PATH="$TMP/bin:$PATH" FAKE_RELEASES="$TMP/releases.tsv" FAKE_DELETED="$TMP/deleted" \
    FAKE_LIST_FAIL=1 bash "$SCRIPT" --repo Daedalusys/daedalus-core --keep 2 >"$TMP/out" 2>&1 || rc=$?
st 1 "$rc" "gh release list 失败 → 退出码 1"
st 0 "$(wc -l <"$TMP/deleted" | tr -d ' ')" "gh release list 失败时什么都没删"

# 待删数超上限 → 拒跑
awk 'BEGIN{for(i=1;i<=60;i++) printf "2026-01-0%dT00:00:00Z\tabcdef%05d\tfalse\tfalse\n", (i%9)+1, i}' \
    >"$TMP/many.tsv"
: >"$TMP/deleted"; rc=0
PATH="$TMP/bin:$PATH" FAKE_RELEASES="$TMP/many.tsv" FAKE_DELETED="$TMP/deleted" \
    bash "$SCRIPT" --repo Daedalusys/daedalus-core --keep 1 >"$TMP/out" 2>&1 || rc=$?
st 1 "$rc" "待删 59 个 > 上限 50 → 拒跑"
st 0 "$(wc -l <"$TMP/deleted" | tr -d ' ')" "超上限时什么都没删"

if [ "$FAIL_N" -eq 0 ]; then echo "RESULT: PRUNE-RELEASES-TEST PASS"; exit 0; fi
echo "RESULT: PRUNE-RELEASES-TEST FAIL ($FAIL_N)"; exit 1
