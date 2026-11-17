# CD 靶机上线：`Mastwet_Server` 自托管 runner 安装清单

- status: executed（2026-11-06 清单已执行：runner 已注册在 Masterwet_Server，标签 `daedalus-kvm`；daedalus-core/.github/workflows/build.yml:833/921/1013/1163 四条腿自 2026-11-15 起在它上面跑实机验证）
- created: 2026-11-06
- archived-at: 2026-11-17
- 目标：在 `Mastwet_Server`（10.66.0.9）注册一个带 `daedalus-kvm` 标签的 GitHub Actions 自托管 runner，供 `daedalus-core/build.yml` 的 `deploy-verify-kvm` job 跑真 KVM 引导。

## 0. 已实测的前提（2026-11-06，无需复查）

| 项 | 值 |
|---|---|
| OS | Fedora Linux 44，kernel 6.19.10，systemd 259 |
| CPU | i7-12700H，20 核，**x86-64-v3 全指令**（avx2/bmi1/bmi2/fma/movbe/xsave） |
| KVM | `/dev/kvm` `crw-rw-rw-`（免 sudo 可写） |
| 磁盘 | `/` 451 G 可用（NVMe） |
| 内存 | 15 GiB |
| 已在位 | podman / qemu-system-x86_64 / qemu-img / just / git / deno |
| 登录用户 | `udefine-dev`（在 `wheel` 组，但 `sudo` 需密码） |
| 缺 | `bootc`/`ostree`/`rpm-ostree`；免密 sudo；runner |

## 1. 免密 sudo（解 `deploy-verify-kvm` 的 `sudo bash bootc-verify.sh`）

用**专用运行用户**比给 `udefine-dev` 开免密更干净。二选一：

**方案 A（推荐，专用用户）**
```bash
sudo useradd -m -G wheel,kvm daedalus-runner
echo 'daedalus-runner ALL=(ALL) NOPASSWD: ALL' | sudo tee /etc/sudoers.d/daedalus-runner
sudo chmod 440 /etc/sudoers.d/daedalus-runner
```

**方案 B（复用 `udefine-dev`）**
```bash
echo 'udefine-dev ALL=(ALL) NOPASSWD: ALL' | sudo tee /etc/sudoers.d/udefine-dev
sudo chmod 440 /etc/sudoers.d/udefine-dev
```

> 风险提示：runner 上会执行构建产物。**只对 main/tag/出货档触发**（workflow 已这么限定）；不要给 PR 开放该 runner。

## 2. 装 bootc/ostree（P2 的 M1 用；P1 引导不需要，但先装上省事）

```bash
sudo dnf install -y bootc ostree
```

## 3. 注册 runner（repo 级，标签 `daedalus-kvm`）

用 `gh` 取一次性注册 token（`gh` 已登录为 `loficore`，有 `Daedalusys/daedalus-core` 管理权）：

```bash
TOKEN=$(gh api -X POST repos/Daedalusys/daedalus-core/actions/runners/registration-token --jq .token)
```

在 `Mastwet_Server` 上：

```bash
mkdir -p ~/actions-runner && cd ~/actions-runner
RUNNER_VER=2.329.0   # 用 https://github.com/actions/runner/releases 的最新版
curl -o runner.tar.gz -L \
  "https://github.com/actions/runner/releases/download/v${RUNNER_VER}/actions-runner-linux-x64-${RUNNER_VER}.tar.gz"
tar xzf runner.tar.gz

./config.sh \
  --url https://github.com/Daedalusys/daedalus-core \
  --token "$TOKEN" \
  --name masterwet-kvm \
  --labels daedalus-kvm \
  --unattended \
  --work _work
```

> 用**你指定的运行用户**跑 `config.sh`（方案 A 则先 `sudo -u daedalus-runner -i`）。

## 4. 装成 systemd 服务（开机自启）

```bash
cd ~/actions-runner
sudo ./svc.sh install "$USER"
sudo ./svc.sh start
sudo ./svc.sh status
```

## 5. 验证

```bash
# runner 已注册且 idle
gh api repos/Daedalusys/daedalus-core/actions/runners \
  --jq '.runners[] | "\(.name) \(.status) \(.labels|map(.name)|join(","))"'
```

期望出现 `masterwet-kvm online self-hosted,linux,x64,daedalus-kvm`。

靶机侧自检（不需要 GitHub）：
```bash
sudo bash daedalus-core/tests/real-machine/bootc-verify.sh --only-preflight
# 期望:CPU 支持 x86-64-v3 / /dev/kvm 可读写 / 退出码 0
```

## 6. 触发一次真验证

```bash
# 出货档 + 出 qcow2 + 跑 KVM 引导
gh workflow run build.yml --repo Daedalusys/daedalus-core --ref main -f VARIANT=headless -f PUSH=true
# 或对已有 main 推送等待自动触发(出货档条件见 build.yml)
```

然后看 `deploy-verify-kvm` job：绿 = 镜像真能引导；证据 artifact = `$WORK`（`serial-console.log` / `bootc-verify.log` / `lsblk-*.txt` / `lsblk.diff`）。

## 7. 回滚/卸载

```bash
cd ~/actions-runner && sudo ./svc.sh stop && sudo ./svc.sh uninstall
./config.sh remove --token "$(gh api -X POST repos/Daedalusys/daedalus-core/actions/runners/remove-token --jq .token)"
```
