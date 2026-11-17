# copilot 进 OS 层沙箱 —— 技术探针证据(core#117,S1–S5)

**探针**:`tests/real-machine/probe-copilot-sandbox.sh`(只读:临时瞬时单元用完即退,写入只落
`$WORK` 与 `/var/tmp`,探针自建的 `/usr/local/bin/deno-probe117` 在退出时删除)
**靶场**:`Masterwet_Server`(`undef-dev`,Fedora 44,`/dev/kvm` 可写,`sudo` 免密)
**环境三元组**:`systemd 259 (259.10-1.fc44)` | `deno 2.9.5 (stable, release, x86_64-unknown-linux-gnu)` | `kernel 7.3.3-200.fc44.x86_64`
**运行方式**:同一份脚本跑两次 —— 普通用户(user manager,uid 1000)与 `sudo`(system manager,pid1)
**原始输出**:`probe-user-manager.log`(user manager 全量 stdout)、`probe-system-manager.log`(system manager 全量 stdout)

## 版本绑定(不可外推)

镜像钉的是 **deno 2.9.8**(`files/scripts/deno-version.env` 的 `DENO_VERSION`),靶场是 **2.9.5**。
两条结论显式依赖版本,**不要跨版本外推**:

1. `deno eval` 的权限旗标行为:2.9.5 上 `--allow-write` / `--deny-write` 直接 scolded
   (`error: unexpected argument '--allow-write' found`),命令根本没跑起来;2026-11-15 那次在
   2.9.8 上则是接受旗标但不施加边界。**两种版本下旧判据都不可用,但失效原因不同。**
2. V8 被 `MemoryDenyWriteExecute=yes` 杀死时的 panic 文案:2.9.5 =
   `Check failed: success || errno == ENOMEM.`;2.9.8 上是 `Check failed: 12 == (*__errno_location ())`。
   判定为「W^X 杀死 V8」这件事两版一致,但**逐字证据只对各自版本成立**。

结论一律读作「在 systemd 259 + deno 2.9.5 + kernel 7.3.3-200.fc44 上成立」。

## 逐条判定

| 用例 | user manager(uid 1000) | system manager(root,pid1) |
|---|---|---|
| **S1-plan** 计划原文形态(DynamicUser + ProtectSystem + seccomp + W^X) | NOTE BLOCKED(需 root:瞬态系统单元要求交互认证) | **FAIL** 被 seccomp 打死 —— `Main processes terminated with: code=dumped, status=31/SYS` |
| **S1** 只 `MemoryDenyWriteExecute=yes` | **FAIL** V8 panic `Check failed: success || errno == ENOMEM.` | **FAIL** 同上 |
| **S1** 只 `SystemCallFilter=@system-service` | **FAIL** SIGSYS(`status=31/SYS`) | **FAIL** 同上 |
| **S1** 逃生门(W^X + seccomp + `--v8-flags=--jitless`) | **PASS** `JIT_OK true` | **PASS** 同上 |
| **S2** `systemd-run --scope` 带沙箱属性 | **FAIL** 6/6 条 `Unknown assignment` | **FAIL** 6/6 条(与 root 无关,是 scope 单元本身不支持) |
| **S2** service 形态 seccomp | **PASS** `Seccomp: 2` | **PASS** `Seccomp: 2` |
| **S2** service 形态 `ProtectSystem=strict` | **PASS** `/` 为 `ro` | **PASS** `/` 为 `ro` |
| **S3** 测法自检(无 pty) | **PASS** `ISATTY false false false` | **PASS** 同上 |
| **S3** service 形态 TTY(`--pipe`,调用方为真 pty) | **PASS** `stdin/stdout isTerminal=true` | **PASS** 同上 |
| **S3** service 形态 Ctrl-C(pty 忠实模拟) | **FAIL** 前端 `signal:2`,单元内进程存活=yes、SIGINT 送达=0 → 孤儿 | **FAIL** 同上 |
| **S3** scope 形态 Ctrl-C | NOTE BLOCKED(该形态带不了沙箱属性) | NOTE BLOCKED(相同;实测 `code:130`、送达=1、存活=no) |
| **S4** `IPAddressDeny=any` / `IPAddressAllow` | **FAIL** 同一可达 IP 在只开 deny 时仍 `200` —— 过滤器压根没装上 | **PASS** 同一 IP 200 → 000 → 200 翻转 |
| **S5** 旧 D3 判据(`deno eval` + 期望 `PermissionDenied`) | **FAIL** 命令没跑起来(不接受权限旗标) | **FAIL** 同上 |
| **S5** 生产形态(`deno run`) | **PASS** `NotCapable`,且 `--allow-write=/` 即成功 | **PASS** 同上 |
| **S5** `'$HOME'` 字面量在 `deno run` 下 | **PASS**(不展开 → `NotCapable`;写死绝对路径才有效) | **PASS** 同上 |
| 汇总 | 7 PASS / 6 FAIL / 3 BLOCKED | 8 PASS / 6 FAIL / 1 BLOCKED |

> `FAIL` = 被检验的提议不成立,**不是探针故障**;探针自身不可信会表现为 `NOTE BLOCKED` 或缺输出。

## 四个待定问题的答案

### 1. stdio 是否保得住

**分两半答:TTY 保得住,信号链保不住。**

- TTY:`systemd-run --unit --pipe` 在调用方是真终端时,单元内 `Deno.stdin.isTerminal()` /
  `stdout.isTerminal()` 均为 `true`(两种 manager 都 PASS)。测法自检:同一探针在没有 pty 时
  读到 `false`,说明读数不是恒真。
- 信号:pty 里对前台进程组发 SIGINT(等价终端 Ctrl-C)后,`--pipe` 形态**前端被 SIGINT 杀死,
  但单元内进程仍存活且从未收到 SIGINT**:

  ```
  pipe     前端退出=signal:2 单元内进程存活=yes SIGINT 送达单元=0 单元内 pid=382441
  FAIL S3 Ctrl-C 停不住沙箱内进程:--pipe 形态 前端被 SIGINT 杀死,但单元内进程仍存活且从未收到 SIGINT(送达=0)→ 前端与单元脱钩,退出会留孤儿
  ```

  反过来 `--scope` 形态能传递信号(`code:130`、送达=1、无孤儿),但 scope 带不了沙箱属性(S2)。

- 结论:**能带沙箱的形态(service)不转发 Ctrl-C;能转发信号的形态(scope)带不了沙箱。**
  原提议「wrapper 经 scope 进沙箱」双重不成立;要做就得在 wrapper 侧做信号代理,或改成
  常驻 broker + 前端 CLI 两段式。

### 2. provider 域名清单放哪

**放 `policy.toml`,但前提是 copilot 变成 root 的 system service —— 用户态拿不到这条边界。**

证据(system manager,root):

```
无沙箱(名字解析钉到确定 IP): example.com(172.66.147.243)=200
IPAddressDeny=any 下访问钉住的 example.com(172.66.147.243)→ curl: (28) Connection timed out after 6002 milliseconds deny_any=000
IPAddressDeny=any + IPAddressAllow=172.66.147.243(正:example.com / 反:github.com)→ allow=200 curl: (28) Connection timed out after 6002 milliseconds deny=000
PASS S4 IP 层白名单真生效:同一 IP(example.com=172.66.147.243)无沙箱 200 → IPAddressDeny=any 下不可达(deny_any=000)→ 加入 IPAddressAllow 后 200(allowed=200),非白名单 github.com 仍不可达(system manager(root))
```

同一个 IP 的 200 → 000 → 200 翻转只可能由该属性造成,不依赖对照主机是否可达。

而 user manager 下同一条属性**根本没生效** —— 同一个可达 IP 在只开 deny 时仍是 200
(过滤器压根没装上):

```
IPAddressDeny=any 下访问钉住的 example.com(172.66.147.243)→ deny_any=200
IPAddressDeny=any + IPAddressAllow=172.66.147.243(正:example.com / 反:github.com)→ allow=200 deny=200
FAIL S4 user manager 下 IPAddressDeny=any 未生效:同一个可达 IP 仍 200(deny_any=200),unprivileged_bpf_disabled=2 → copilot 以用户身份跑时部署不了 BPF 网络白名单
```

清单本身进 `policy.toml`(运维侧可改),与现有 `[shell]` / `[fs]` 同源;`policy.go` 对未知键
fail-closed,加 `[net]` 节要同步 SDK struct + 三点漂移测试 + `76` 的必需节预检。manifest 只有
`--allow-net`(无白名单,`plugin/copilot/daedalus.plugin.json:13`),保留为 defense-in-depth。

### 3. 与决策 16「宿主零 spawn」的关系

**不越界,但要写进文档。** 实测把 `--scope` 形态否掉了(S2 六条属性两种 manager 全
`Unknown assignment`),所以只剩「由 pid1 起 unit」这一条路:`systemd-run` / `systemctl` 由
**wrapper 或服务侧**调用,`daedalus-host run-plugin` 依旧只打印 argv、仍是零 spawn。
决策 16 约束的是「宿主不得成为 MCP 服务器的父进程」,与此不冲突 —— 但正因为它只约束宿主,
下一次评审很容易把「wrapper 调 systemd-run」误判成违例,故必须在 ARCHITECTURE / wrapper 注释里
明写一句。

### 4. Landlock 是否真启用

**没有启用。** `daedalus-*.service.d/landlock.conf` 只有 `SystemCallFilter`(seccomp)行,
没有任何 `LandlockReadOnlyPaths` / `LandlockReadWritePaths`;路径约束实际由
`ProtectSystem=strict` 承担 —— 本轮实测确认它真的生效(`/` 为 `ro`,两种 manager 都是):

```
S2-service(ProtectSystem=strict + seccomp)逐字:
      Seccomp:	2
      ro,relatime,seclabel,compress=zstd:1,ssd,discard=async,space_cache=v2,subvolid=256,subvol=/root
```

要真上 Landlock LSM(路径级、内核强制、与运行时无关)是独立评估,不是本 issue 的前置。

## 判决

**能做 —— 但只能按下面这个最小可行形态做,且原提议的三处细节必须改。**

最小可行形态:

1. **root 的 system service**(`daedalus-copilot.service`,或瞬态 `systemd-run --unit=… --pipe`),
   **不是 `systemd-run --scope`**(scope 六条属性全拒,与 root 无关)。
2. **copilot 专属沙箱 drop-in**,不能复用其它插件那份:
   - `MemoryDenyWriteExecute=yes` → **必须去掉**,或给 Deno 加 `--v8-flags=--jitless`
     (W^X 下 V8 直接 panic;jitless 是唯一实测存活的逃生门);
   - `SystemCallFilter=@system-service` → **必须配 `SystemCallErrorNumber=EPERM`**(默认动作
     SIGSYS 会直接杀 deno,`status=31/SYS`;EPERM 化后同一 filter 存活),或把 `pkey_alloc`
     等缺失 syscall 显式加回白名单。
3. **网络白名单可落地**:`IPAddressDeny=any` + `IPAddressAllow=<provider IP>`,只在 system
   manager 下有效;域名清单进 `policy.toml`(需先做 provider 域名 → IP 的解析策略,BPF 只认 IP)。
4. **TTY 与信号分开解**:**`--pipe` 保住 TTY,但不转发 Ctrl-C(实测孤儿)** → wrapper 侧信号
   代理,或 broker 两段式(常驻服务 + 前端 CLI)。
5. **manifest 的 `--allow-*` 保留**,降为 defense-in-depth;安全断言由 OS 层承担。

被明确否掉的部分(不要再去试):

- **`systemd-run --scope` 形态**:六条属性两种 manager 全 `Unknown assignment`。
- **用户态(user manager)下的网络白名单**:静默无效,`unprivileged_bpf_disabled=2`。
- **计划原文那条命令原样使用**:同时开 seccomp 默认动作与 W^X 会杀死 deno
  (`status=31/SYS`;摘掉 seccomp 后是 `Check failed: success || errno == ENOMEM.`)。

一条实现期的操作事实:system manager 的单元跑在 SELinux `unconfined_service_t` 域里,**能读
但不能执行 `user_home_t` / `user_tmp_t` 文件**(实测 `203/EXEC`,日志
`Unable to locate executable ...: Permission denied`)。所以 copilot 的可执行文件必须落在
`bin_t` 目录(镜像里就是 `/usr/local/bin`,天然满足);探针为此把 deno 复制到
`/usr/local/bin/deno-probe117` 再用,退出即删。

## 探针自身修正(三处假绿 + 一处对照设计)

这一轮同时修掉了探针自己的说谎点 —— 它们都会把「环境/测法问题」写成 `PASS` 或错误归因:

1. **S1-seccomp**:旧写法匹配 `code=dumped/status=SYS`,而 systemd 259 实际排版是
   `code=dumped, status=31/SYS` → 匹配失败落进 `else`,把「SIGSYS 崩掉」报成
   `PASS S1 纯 seccomp 未额外致死`。现按 `status=[0-9]+/SYS` 判,且正向必须有 `JIT_OK`。
2. **S5**:旧 `else` 分支把「deno eval 不接受权限旗标、命令没跑起来」报成
   `PASS 旧 D3 形态可用`。现显式识别 usage 错并判 `FAIL`。
3. **S3 Ctrl-C**:旧写法用 `&` 起前端,非交互 shell 会把 SIGINT 继承成 `SIG_IGN`
   (实测 `SigIgn=0000000000000006`)→ 单元永远收不到 SIGINT,看似「systemd 不转发」。现改用
   pty(`pty.fork()` 让前端成为带控制终端的会话首进程,再对前台进程组发 SIGINT)忠实模拟终端
   Ctrl-C。**结论仍是 FAIL,但这次是 systemd 的真实行为,不是 shell 产物。**
4. **S4 对照**:裸域名测不出「DNS 被拦」与「IP 被拦」的区别(`IPAddressDeny=any` 先把 53 拦掉)。
   现用 `curl --resolve` 把解析钉在本地。另修掉一个差点混进来的假绿:判据一度只看
   「白名单 IP 可达 + 对照 IP 不可达」,而 user manager 下对照主机恰好瞬时不通时 `deny=000`
   被当成了「白名单生效」—— 实际同一 IP 在只开 deny 时仍是 `200`(过滤器没装上)。现在
   **先**用「同一可达 IP 的开/关」做门禁(`deny_any=200` 直接判未生效),再谈正反对照。
5. **S0 前提守卫**:`unit 内 deno` 拿到的是 systemd 报错(`Failed to find executable …`)时
   旧判据只看「非空」,于是后面每条用例产出一堆 FAIL;现已要求必须匹配 `^deno [0-9]`,
   否则一条 `NOTE BLOCKED` 后退出。三条缺前提路径(无 deno / deno 跑不起来 / 非 root 的
   system manager)都实测打 `NOTE BLOCKED`,无一条折算成 PASS。

## 复现

```bash
ssh Masterwet_Server        # Fedora 44 / systemd 259 / deno 2.9.5
cd ~/daedalus-e2e/p2-probe

# user manager 形态
bash tests/real-machine/probe-copilot-sandbox.sh
# system manager 形态(root;探针自己处理 SELinux bin_t 中转与清理)
sudo bash tests/real-machine/probe-copilot-sandbox.sh
```

退出码:`0` = 无 FAIL(BLOCKED 不算失败),`1` = 有 FAIL。两轮实测均为 `1`(6 个 FAIL),
与本文判定表一致。
