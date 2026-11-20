# qcow2 压缩:试过,不划算 —— 负结果记录(core#196,2026-11-20)

| 项目 | 内容 |
| --- | --- |
| **status** | 已结论:**不做**(曾于 PR #209 落地,同日回退) |
| **触发 issue** | [#196](https://github.com/Daedalusys/daedalus-core/issues/196) —— 出货资产 1.24 GiB、实测 ~55 KB/s,下载要 6～7 小时 |
| **实验** | 在 `build-qcow2` 里用 `qemu-img convert -O qcow2 -c` 压 zlib 簇后再发布(压缩后必须更小才采用) |
| **结论证据** | 2026-11-20 main 出货 run [44707815113](https://github.com/Daedalusys/daedalus-core/actions/runs/44707815113) 的 `build-qcow2` 日志 |

## 实测数字(真跑,不是估算)

```
qemu-img info: virtual size 10 GiB / disk size 1.22 GiB / compression type: zlib
qcow2 compressed: 1332346880 bytes -> 1321271296 bytes (ratio 0.992x, saved 0.8%)
qcow2 final: 1321271296 bytes
```

对照上一版资产:`qcow2-v0.1.1.qcow2` = 1,336,803,328 字节。

| | 体积 | @55 KB/s 预计下载 |
| --- | --- | --- |
| 未压缩 | 1.24 GiB | ≈ 6.7 小时 |
| 压缩后 | 1.23 GiB(−0.8%) | ≈ 6.6 小时 |

**压缩后的镜像能正常开机**:同一 run 的 `Deploy Verify (real KVM boot)` 引导的就是压缩后的字节,
31m21s 通过(上一次未压缩 run 35m46s,run-to-run 波动范围内)。

## 为什么压不动

bootc 镜像的根文件系统在镜像里**已经是压缩过的**(squashfs/erofs 类),几乎全是高熵数据;
再套一层 zlib 只能挤出 0.8%。换成 zstd/xz 也同理 —— 已压缩的数据没有可再压的空间。

## 裁决

- **回退压缩腿**(保留 #191 的 ISO 改名)。理由:0.8% 的收益换来一个新的 CI 失败点
  (`convert`/`check` 失败即红)、一次额外 `qemu-utils` 安装、每台用户机器多一次读盘解压 —— 不值。
- **#196 的真正瓶颈是下载通路,不是体积**:直连 GitHub release 约 55 KB/s,而同一时刻
  第三方加速站 1.3–1.9 MB/s(≈12–15 分钟)。
- 已落地的对症措施:构建流水线现算 `SHA256SUMS`(v0.1.1 起)、quickstart/release notes
  写明加速站路径与校验要求。
- 剩余杠杆(**未做,属基础设施决策**):换托管/镜像源(对象存储直链、自建镜像站),
  或在镜像构建期换更省空间的 squashfs 压缩算法(收益有限,且改动面在镜像侧)。

## 复现方式

```bash
# 压缩(仅用于核算比例;qemu 直接引导压缩 qcow2,不需要解压)
qemu-img convert -O qcow2 -c disk.qcow2 disk.compressed.qcow2
qemu-img info disk.compressed.qcow2 | grep -E 'disk size|compression type'
stat -c %s disk.qcow2 disk.compressed.qcow2
```
