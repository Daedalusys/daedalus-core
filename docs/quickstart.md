# 第一次跑起来(v0.1.0)

这是 **alpha 试用镜像**,不是生产系统。它跑在 QEMU/KVM 里,自带一个**公开口令**的试用
账号 —— 默认 **不要**把它暴露到公网,也不要拿它装真机。

前提:一台 x86-64-v3 的 Linux(2015 年后的 Intel/AMD 基本都算)、`/dev/kvm` 可写、装了 `qemu`。
不需要 root(能读 `/dev/kvm` 即可)。

## 1. 下载

```bash
base=https://github.com/Daedalusys/daedalus-core/releases/download/v0.1.0
curl -fLO "$base/qcow2-v0.1.0.qcow2"
```

同页的 `iso-v0.1.0.iso` **不是安装器**:它是 CI 的验证夹具(开机自动装到 `/dev/vda`,
live 里有一个构建期临时账号),见
[#191](https://github.com/Daedalusys/daedalus-core/issues/191)。

## 2. 起机

```bash
qemu-system-x86_64 -machine q35,accel=kvm -cpu host -m 4096 -smp 2 \
  -drive file=qcow2-v0.1.0.qcow2,if=virtio,format=qcow2 \
  -netdev user,id=net0,hostfwd=tcp::2222-:22 -device virtio-net,netdev=net0 \
  -nographic
```

串口会走到 `login:`;也可以直接 SSH:

```bash
ssh -p 2222 daedalus@127.0.0.1     # 口令:daedalus
```

账号 `daedalus` 有免密 sudo;`root` 也是同一个口令。**alpha 试用凭据,第一件事改掉**
(`passwd`、`sudo passwd root`)。要一个不带账号的干净镜像,按 `README` 自己 `just build`
(不传 `DAEDALUS_DEV_USER`/`DAEDALUS_DEV_PASS` 即可)。

## 3. 看它有什么

```bash
daedalus-host list        # 14 个插件,status 列应全是 ok
sudo daedalus-smoke       # 镜像自带的端到端自检,最后一行 SMOKE OK
sudo bootc status         # 原子部署:当前部署 + 可回滚的上一个部署
```

## 4. 做一次「有回滚点」的变更

事务是 Daedalus 的默认变更通道:`begin → propose → apply → rollback`,每步进 journal,
每步盖哈希链审计。

```bash
mkdir -p ~/.config/systemd/user
printf '[Unit]\nDescription=demo\n[Service]\nExecStart=/bin/sleep 300\n[Install]\nWantedBy=default.target\n' \
  > ~/.config/systemd/user/demo.service
systemctl --user daemon-reload

tx=$(daedalus-tx begin | sed 's/.*"tx_id":"\([a-f0-9]*\)".*/\1/')
daedalus-tx propose "$tx" service.set '{"name":"demo.service","desired_state":"enabled"}'
daedalus-tx apply "$tx"
systemctl --user is-enabled demo.service      # → enabled

daedalus-tx rollback "$tx"
systemctl --user is-enabled demo.service      # → disabled
sudo daedalus-audit verify --log-path /var/log/daedalus/audit.jsonl   # → hash chain intact
```

## 5. 让 AI 帮你翻译一句话

```bash
export DAEDALUS_LLM_API_KEY=...
daedalus --dry-run "看一下系统版本"      # 只翻译,不执行
daedalus "看一下系统版本"                # L0 会问 y/n,然后在沙箱里真跑
```

风险分级由**本地静态分类器**判定,模型不会自己标风险;L1/L2 只展示、不执行。
可选配置:`DAEDALUS_LLM_PROVIDER`(`openai`|`anthropic`)、`DAEDALUS_LLM_MODEL`、
`DAEDALUS_LLM_BASE_URL`(自建 OpenAI 兼容网关时填,例如 `http://<host>:<port>/v1`)。

## 6. 必须知道的边界

- **这款镜像是 `daedalus-atomic` 节点**:`/usr` 与 `/opt` 都是只读 composefs。
  包变更与系统变更走 **bootc**(`bootc upgrade` → 新部署 → `bootc rollback`),不走 dnf。
  在 atomic 节点上调 `daedalus-tx package.set` 会被**明确拒绝** —— 这是设计:
  `package` kind 只适用于 `daedalus-agent` 节点(普通发行版)。
- **`policy.set` 目前也写不进去**(真源 `/opt/...` 只读),见
  [#188](https://github.com/Daedalusys/daedalus-core/issues/188)。
- **审计**:root 身份写的审计落 `/var/log/daedalus/audit.jsonl`;普通用户身份目前会静默写失败,
  见 [#189](https://github.com/Daedalusys/daedalus-core/issues/189)。做「带审计的变更」请用 root。
- **copilot 在缺 key / 配置错误时仍以 rc=0 退出**,见
  [#190](https://github.com/Daedalusys/daedalus-core/issues/190) —— 脚本里别拿 rc 判成败。

## 自己验一遍

```bash
sudo VERIFY_WORK=/tmp/probe-first-run \
  VERIFY_IMAGE=ghcr.io/daedalusys/daedalus-os:latest \
  bash tests/real-machine/probe-first-run.sh
```

它在 KVM 宿主上派生一次性镜像、跑完 T0–T5 六段并给 PASS/FAIL。需要 rootful podman,
并在 `$VERIFY_WORK` 下建约 1.3G 的 qcow2(别指向已有数据的目录)。

有 LLM key 时,copilot 真链路另有两条探针(推荐,key 走文件、不进命令行):

```bash
sudo DAEDALUS_LLM_KEY_FILE=$HOME/.daedalus-llm-key \
     DAEDALUS_LLM_MODEL=<model> DAEDALUS_LLM_BASE_URL=<base-url> \
     bash tests/real-machine/probe-copilot-keyed.sh
```
