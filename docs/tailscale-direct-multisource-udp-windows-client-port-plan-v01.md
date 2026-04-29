# Tailscale Direct Multi-Source UDP — Windows Client Port Plan v01

Date: 2026-04-29

Repository: `https://github.com/fullcone/multiport`

Local checkout: `C:\other_project\zerotier-client\multiport`

WSL checkout: `/mnt/c/other_project/zerotier-client/multiport`

Source PR (Linux server, Final Closeout): `#1`

Target PR (Windows client): not yet opened

Base commit for this plan: `5f253bf78672d068d0cf283f695df5f66cc2cdfe`

## 0. Role Split and PR Scope

Project-level role split confirmed by user:

- **Windows = client target platform** (用户侧客户端)。
- **Linux = server target platform** (服务端中继/落地)。

PR #1 已在 Phase 16 Final Closeout 完整交付 Linux 服务端 srcsel。本 PR (PR #2) 不
是「Linux PoC 之后的扩展」，而是把 PR #1 已经成熟的核心算法/scorer/safety
budget/metrics 移植到 Windows 客户端，使**反向路径**（client → server）也能做
source socket 选择，闭合 v04 旧设计文档点名的 "Asymmetric ECMP reverse path"
风险。

In-scope:

- Windows amd64/arm64 上 srcsel auxiliary socket 创建、IPv4 + IPv6 双栈。
- Windows 上 forced data source 与 automatic source selection 全路径（与 Linux
  对齐）。
- Windows 上的 probe Ping/Pong、scorer、safety budget、debug snapshot、metrics。
- Windows 平台风险（Windows Firewall、Wintun、Modern Standby、IOCP）的验证。
- Windows ↔ Linux 双端实网联调（双向 source-aware data send 都生效）。
- Codex 审计闭环，与 PR #1 同样的 Phase 文档体例。

Out-of-scope:

- macOS / BSD source-aware send（独立 PR）。
- Per-NIC source selection（多 NIC 客户端在不同物理接口绑 aux socket）—— v02 没
  覆盖、PR #1 没做、Windows 客户端虽然多 NIC 概率更高，但放在后续设计 PR 里。
- 修改 PR #1 已固化的 Linux 路径 —— 任何为 Windows 引入的共享代码必须保持 Linux
  侧二进制行为不变（差量验证）。
- Lazy endpoint 走 source-aware 发送 —— PR #1 Phase 14 已明确保持 primary，本 PR
  延续此约束。
- 真实 production rollout，仅交付实验开关 + 双端验证。

## 1. v02 设计文档在 client/server 角色分工下的重读

`tailscale_direct_multisource_udp_final_implementation_v02.md` 写于角色分工
明确之前，整体把两端当作对称 peer。以下章节需要在 Windows PR 立项时重新
解读。**没有列出的章节默认仍按 v02 原文执行。**

| v02 章节 | 原文立场 | 本 PR 重解 |
| --- | --- | --- |
| § 6 接收路径 | 把 raw disco 列为 Phase 1 必处理 blocker | Windows 不存在 raw disco 路径（`magicsock_linux.go` 是 `linux && !ts_omit_listenrawdisco`），blocker 自动消解；保留 `handleDiscoMessageWithSource` 调用点不变 |
| § 7 发送路径 § 13 文件清单 | 仅列 `magicsock_linux.go` | 新增 `sourcepath_windows.go`（或将 `sourcepath_linux.go` 的 build tag 放宽，见 § 2），不需要新增 `magicsock_windows.go` |
| § 8 Scorer 默认值 | probe interval 10s、max peers 32、min hold 30s | Linux server 数值偏保守；Windows client 由于 NAT/Wi-Fi/4G 切换更频繁，**保持 v02 默认值**作为兼容口径，但要把这些值通过环境变量暴露的能力（PR #1 已实现）作为可调旋钮。**第一版不改默认。** |
| § 9 Phase 6 "扩展范围" | "跨平台不能和 Linux PoC 并行推进" "Phase 1-5 都稳定后考虑 Windows" | Linux PoC 早已 Final Closeout，前置条件已满足；Windows PR 现在是合法并行路径，不再受这一节约束 |
| § 11 实网验证 | "easy NAT / single-side hard NAT / double hard NAT" | 这些 NAT 场景**主要属于客户端侧**（Linux server 通常公网或稳定 NAT）。Windows PR 必须把所有 NAT 场景都跑一遍；Linux PR #1 只跑了 loopback 双 node，不构成对真实 NAT 行为的验证 |
| § 12 风险表 | "Windows 行为不稳定 / Linux-only 起步" | 该行原本是「将来」，现在是「当下」；本 PR 必须替换为具体 Windows 风险清单（见 § 3） |
| § 14 Q1-Q6 实施门槛 | 6 个 Phase 1 前置问题 | 全部已在 Linux 闭环中给出答案。Windows PR 沿用同一答案：wrapper 兼容签名 / 双栈 / 允许 non-batch / hard NAT 默认仅观测 / debug HTTP 默认脱敏 / lazyEndpoint 保持 primary |
| § 15 一句话执行版 | "Linux-only PoC 验证可行性" | 本 PR 一句话执行版改为："把 PR #1 已 Final Closeout 的 srcsel 移植到 Windows 客户端，沿用所有算法、scorer、safety budget 与默认值，用 build tag 拆分 + Wintun/Firewall 风险清单 + 双端实网联调闭环" |

## 2. Build Tag 拆分策略

### 现状

```text
sourcepath.go         (no tag, all platforms)        类型与共享逻辑
sourcepath_default.go //go:build !linux              全 stub，aux socket 不创建
sourcepath_linux.go   //go:build linux               aux socket 全实现
sourcepath_test.go    (no tag, all platforms)        共享测试
sourcepath_linux_test.go //go:build linux            Linux 集成 + 双 node 运行时测试
magicsock_linux.go    //go:build linux && !ts_omit_listenrawdisco   raw disco
magicsock_default.go  //go:build !linux || ts_omit_listenrawdisco  默认（含 Windows）
```

### 推荐方案 A（首选）：tag 放宽

把 `sourcepath_linux.go` 重命名为 `sourcepath_unix.go`（或 `sourcepath_posixish.go`），
build tag 改为 `linux || windows`。`sourcepath_default.go` 收紧为
`!linux && !windows`。

理由：

1. `sourcepath_linux.go` 254 行实际**零 Linux 专属 syscall** —— 只用 magicsock
   跨平台抽象 (`c.listenPacket`、`RebindingUDPConn`、`envknob`)。
2. 一份代码两平台共维护，避免 Windows 和 Linux 行为漂移。
3. 任何 Windows 上发现的真正平台分歧（例如 GSO batching 行为）通过运行时
   检测或新建 `sourcepath_windows.go` 增量覆盖，而不是从一开始就分叉。

### 备选方案 B：复制分叉

直接复制 `sourcepath_linux.go` → `sourcepath_windows.go`，build tag 各自独立。

理由仅在以下情况成立：

- Windows 出现需要不同 socket option / batching 策略（目前未验证有此需要）。
- 想保留 Linux 二进制完全 unchanged 的强保证（方案 A 也保留，只要差量测试通过）。

### 决定

**先用方案 A**，配 § 3 风险清单的实测结果。如果第一次实测发现 Windows 上需要
不同 socket 行为再退回方案 B。

### 测试 build tag

`sourcepath_linux_test.go` 同步处理：

- 重命名为 `sourcepath_unix_test.go`，build tag `linux || windows`。
- 把 `syscall.EPERM` 替换为 cross-platform 错误注入（见 § 4）。

## 3. Wintun / Windows Firewall / IOCP / Modern Standby 风险清单

每条按「风险 → 检测方式 → 缓解」三段写。这是 Windows PR 必须验证的清单，对应
v02 § 12 表里被 Linux 跳过的 "Windows 行为不稳定" 那一行。

### 3.1 Windows Firewall (mpssvc) 对 aux socket 的处理

**风险**：tailscaled.exe 已对 primary UDP socket 取得 firewall 入站豁免；新增
auxiliary UDP socket（不同本地端口）是否会触发新 prompt 或被默认拒绝？这会
直接破坏 srcsel 的探针/数据收发。

**检测**：

```powershell
# 启用 srcsel 后启动 tailscaled，立即查 firewall rule
Get-NetFirewallApplicationFilter -Program "*tailscaled*" |
  Get-NetFirewallRule | Select-Object DisplayName, Direction, Action, Profile
```

加 aux socket 后从 `netstat -ano` 观察新 UDP 端口是否被绑定且收得到对端 disco
Pong。

**缓解**：

- 第一版假定 firewall rule 是 application-scoped（基于 tailscaled.exe 路径），
  那么任意 UDP 本地端口都已豁免 —— 这是 Tailscale 当前 stock installer 的
  正常行为。
- 如果发现 firewall 是 port-scoped，需要 installer 改造；此情况下本 PR 退回到
  仅在调试场景启用，并在 Phase 文档明确标记此限制。

### 3.2 Wintun TUN 驱动层

**风险**：Wintun 是 Tailscale Windows 客户端使用的 TUN 驱动，处于 L3。srcsel 的
auxiliary socket 在 UDP 层（L4），按理与 Wintun 解耦；但需要验证 Wintun 不会
对来自 aux 端口的 packet 做特殊处理（例如绑定 routing entry 到 primary port
的 outbound interface）。

**检测**：

- WireShark 在 aux 端口抓包，确认 packet 真的从对应物理 NIC 出去，不被 Wintun
  bypass 或回环。
- 验证多 NIC 场景（Wi-Fi + Ethernet 同时启用），aux socket 的 outbound 选择
  与 primary 一致或符合 Windows routing table。

**缓解**：

- Wintun 不参与 outbound UDP socket 路径，预期无影响。本节作为 due diligence
  存在。

### 3.3 Modern Standby (S0ix) / Connected Standby

**风险**：Windows 笔记本进入 Modern Standby 后，UDP socket 状态可能被悄悄
失效。`RebindingUDPConn` 的 rebind 逻辑必须能同时刷新 primary 和 aux socket，
不能只 rebind primary 而 aux 保持 stale。

**检测**：

```powershell
# 强制进入 Connected Standby
powercfg /requests
# 模拟 sleep/wake
rundll32.exe powrprof.dll,SetSuspendState 0,1,0
# 唤醒后立即跑 sourcepath_dual_node 测试
```

**缓解**：

- PR #1 Phase 11 已实现 "disable 后清空 srcsel state" 与
  Phase 13 "auxiliary socket count boundary"。本 PR 需新增 Windows 集成测试：
  sleep/wake 后 aux socket 仍可发包或正确进入 rebind。

### 3.4 IOCP / Windows 网络栈调度

**风险**：Go runtime 在 Windows 用 IOCP 而非 epoll，多 socket 并发收发的延迟
分布与 Linux 不同。可能导致 RTT 测量噪声更大，scorer 的 `min_improvement=10ms`
默认值会判定 aux 路径"无足够改进"而频繁回退 primary。

**检测**：

- Phase 6 metrics 在 Linux 已就位（`source path data send metrics`），Windows
  上跑同一个 dual_node 测试，对比 RTT histogram。
- 必要时在 Windows 上把 `min_improvement` 通过 `TS_EXPERIMENTAL_SRCSEL_*` 环境
  变量调高（PR #1 已支持运行时可调）。

**缓解**：

- 第一版不改默认值，仅在文档记录 Windows 上 RTT 噪声基线。
- 如果实测确实出现频繁切换，Phase 文档建议 Windows 默认 `min_improvement`
  上调到 20ms。

### 3.5 Windows Service vs 用户进程

**风险**：tailscaled 在 Windows 默认作为 Windows Service 运行（LocalSystem）。
service 进程的 UDP socket 行为、firewall 上下文与用户进程不同。开发期通常
以用户进程运行，可能掩盖真实部署下的 socket bind 失败。

**检测**：

- 测试矩阵必须包含 service mode 下的 dual_node 验证，不能只在用户进程下跑。

**缓解**：

- Phase 文档明确要求所有验证命令在 service 模式下复现一次。

### 3.6 多 NIC / Wi-Fi ↔ Ethernet 切换

**风险**：Windows 客户端常见多 NIC（Wi-Fi + Ethernet + 4G dongle）。每张 NIC
可能有不同 outbound IP；当用户拔网线、切 Wi-Fi 时，primary 和 aux socket 是
否同步 rebind？

**检测**：

- 手动断开当前 NIC，观察 primary 和 aux 是否一起进入 rebind，并在新 NIC 上
  重新绑端口。

**缓解**：

- PR #1 已有 generation invalidation 机制，rebind 后旧 sample 失效。Windows PR
  只要确保 rebind 触发条件包括 NIC 切换（这是 Tailscale 现有逻辑，本 PR 不改
  动）。

### 3.7 NAT64 / 464XLAT / IPv6-only 网络

**风险**：部分 Windows 客户端（尤其移动热点）只有 IPv6 栈。IPv4 aux socket
bind 会失败。Linux server 不存在此场景。

**检测**：

- IPv6-only 网络下启动 tailscaled，观察 srcsel 是否优雅降级到「只创建 IPv6
  aux socket」。

**缓解**：

- PR #1 `bindSourcePathSocketLocked` 已用 `sourcePathBindError(err4, err6)`
  逻辑容忍单栈失败。Windows PR 只需验证此路径在 v6-only 真实生效。

### 3.8 AntiVirus / EDR

**风险**：第三方 AV/EDR 可能把多 UDP socket 创建判定为可疑（端口扫描行为）。

**检测**：

- 在 Defender、Kaspersky、CrowdStrike 等典型客户环境下跑功能。

**缓解**：

- 暂不改代码。文档列入 known limitations。Phase 文档说明若客户反馈被 AV 误
  阻断，可通过 `TS_EXPERIMENTAL_SRCSEL_AUX_SOCKETS=0` 关闭。

## 4. 测试迁移路径

### 4.1 文件层

| 当前 (Linux) | 移植后 (Windows + Linux 共享) |
| --- | --- |
| `sourcepath_test.go` (无 tag, 412 行) | 不动 |
| `sourcepath_linux_test.go` (`//go:build linux`, 1375 行) | 改名 `sourcepath_unix_test.go`, tag `linux \|\| windows` |
| `sourcepath_default.go` (`!linux`, 36 行 stub) | 收紧为 `!linux && !windows` |
| `sourcepath_linux.go` (`linux`, 254 行) | 改名 `sourcepath_unix.go`, tag `linux \|\| windows` |

### 4.2 错误注入兼容化

`sourcepath_linux_test.go` 中 5 处用 `syscall.EPERM` 注入 send 失败。Windows
下 `syscall.EPERM` 也定义但 wrap 不一致；改用以下任一：

```go
// Option A: 跨平台错误（Linux 与 Windows 都有）
errSendFailed := &net.OpError{Op: "write", Err: syscall.EACCES}

// Option B: 哨兵错误，scorer 不依赖具体 errno
var errInjectSendFail = errors.New("srcsel test: injected send failure")
```

推荐 Option B —— scorer 只看「send 是否失败」，不解析 errno。Option B 移植
零成本且更稳。

### 4.3 dual-node 验证命令

PowerShell 直接调用 `go test`（不必走 WSL）：

```powershell
$env:CGO_ENABLED = "0"  # Wintun-related cgo 不影响 magicsock 测试
go test ./wgengine/magicsock -count=1
go test ./wgengine/magicsock -run "TestSourcePath(ForcedAuxDualNodeRuntime|AutomaticAuxDualNodeRuntime)" -count=1 -v
```

通过条件与 PR #1 Phase 15 一致：

- 抓包显示 forced aux 真的从 aux 本地端口发出 WireGuard payload。
- 自动选择路径在 IPv4 / IPv6 均通过。
- 注入 send 失败后 fallback 到 primary 成功。
- `lastErrRebind` 不被 aux 失败更新。

### 4.4 实网双端联调

| 场景 | client (Windows) | server (Linux, PR #1) | 期望 |
| --- | --- | --- | --- |
| 全公网（无 NAT） | srcsel ON | srcsel ON | 双向 aux 路径都生效，primary 仍可用 |
| client 单侧 hard NAT | srcsel ON | srcsel ON | client 默认仅观测，不自动切换；server 可继续 aux |
| client + server 都有 NAT | srcsel ON | srcsel ON | 默认仅观测；显式 `FORCE_DATA_SOURCE=aux` 验证可达 |
| client Wi-Fi/4G 切换 | srcsel ON | srcsel ON | rebind 后 generation 递增，旧 sample 丢弃 |
| Modern Standby 后唤醒 | srcsel ON | srcsel ON | aux socket 自动恢复或干净 fallback |
| AV/EDR 启用环境 | srcsel ON | srcsel ON | 不被误阻断；如阻断则 disable 路径生效 |

## 5. Phase 大纲

沿用 PR #1 的命名习惯
`tailscale-direct-multisource-udp-phase{N}-{topic}.md`，但使用 W 前缀避免与
PR #1 的 phase 编号冲突。

| Phase | 主题 | 性质 | 通过条件 |
| --- | --- | --- | --- |
| W0 | 本计划文档 + Linux 差量回归 | doc + diff test | 本文档 review 通过；改 build tag 后 Linux 全套 magicsock 测试通过，与 PR #1 baseline 0 diff |
| W1 | build tag 重命名 + sourcepath_windows.go (方案 A) | 代码 | `go test ./wgengine/magicsock` 在 Linux 与 Windows 都通过；编译期不引入 stub 残留 |
| W2 | Windows IPv4 单 aux socket 集成 | 代码 + 测试 | `TestSourcePath*` 在 Windows 通过；`netstat` 看到 aux 端口；抓包看到 disco Ping 从 aux 出 |
| W3 | Windows IPv6 aux socket | 代码 + 测试 | dual-stack runtime 测试通过 |
| W4 | Windows forced data source + automatic selection | 代码 + 测试 | dual-node runtime test 在 Windows 通过；抓包验证 |
| W5 | Windows Firewall / Wintun / Modern Standby 风险清单实测 | 验证文档 | § 3 中 3.1, 3.2, 3.3 全部记录实测结果；如需缓解则在本 phase 实现 |
| W6 | service mode 下复跑 | 验证文档 | LocalSystem 跑同一组测试通过 |
| W7 | Windows ↔ Linux 双端实网联调 | 验证文档 | § 4.4 测试矩阵至少 4 行通过 |
| W8 | Codex 审计闭环 | review fix | 所有 inline review thread resolved；最后一次 `go test` 通过 |
| W9 | Final Closeout | doc-only | 列出 in-scope 行为保证 + out-of-scope；记录最后一个 runtime-changing commit SHA |

## 6. 验收门槛

PR #2 合并前必须满足：

1. Linux 侧 0 行为差异：`git diff` 不影响 PR #1 已交付的 Linux 二进制行为
   （通过 PR #1 闭环测试集差量复跑）。
2. Windows 侧 IPv4 + IPv6 forced 与 automatic 路径都通过 dual-node runtime
   测试。
3. § 3 风险清单中 3.1 (Firewall)、3.3 (Modern Standby)、3.5 (service mode)
   必须有实测记录；3.2 / 3.4 / 3.6 / 3.7 / 3.8 至少有结论性观察记录（不要求
   缓解代码）。
4. 真实远端 Linux 服务器与 Windows 客户端之间至少完成一组双向 srcsel 数据
   面验证。
5. `TS_EXPERIMENTAL_SRCSEL_ENABLE=0` 在 Windows 上完全恢复原生行为，aux
   socket 不创建。

## 7. Open Questions（进入 W1 前必须答）

复用 v02 § 14 的形式，针对 Windows 新增：

1. build tag 用方案 A（共享文件）还是方案 B（分叉文件）？**本计划推荐 A**，
   等 W2 实测后确认。
2. Windows 是否需要不同的 srcsel 默认值？**本计划推荐保持 v02/PR #1 默认**，
   仅通过环境变量在 Phase 文档记录可调上限。
3. Windows installer 是否需要新增 firewall rule？**本计划假设否**（rule 是
   application-scoped），W5 实测决定。
4. service mode 下的 socket bind 行为是否与用户进程一致？**未知，W6 验证**。
5. Codex 审计 prompt 是否沿用 PR #1 的 `<!-- @codex review -->` 注释体例？
   **本计划默认沿用**，便于审计连续性。

## 8. 与 PR #1 的依赖与隔离

依赖：

- 本 PR 完全依赖 PR #1 已 merge 到 main 才能开始 W1。
- 本 PR 的 base branch 必须是包含 `5f253bf78` 之后的 main。

隔离：

- 不修改 PR #1 已固化的 srcsel 算法、scorer、safety budget、debug snapshot
  代码。
- 本 PR 引入的代码若需要 Linux 上也调整（例如 build tag 改名），必须保证
  Linux 编译通过且全套 magicsock 测试 0 diff。
- 任何为兼容 Windows 而修改的共享文件，commit message 必须以 `magicsock:` 或
  `wgengine/magicsock:` 开头，与 PR #1 commit 体例一致。

## 8a. W0 / W1 实测结果（2026-04-29）

### W0 baseline

`5f253bf78672d068d0cf283f695df5f66cc2cdfe` 通过 WSL 跑 `go test ./wgengine/magicsock -count=1`
得到 `ok 11.099s`，与 PR #1 Phase 16 closeout 记录的 11.166s 在抖动范围内一致。

### W1 build tag 拆分

- `wgengine/magicsock/sourcepath_linux.go` → `sourcepath_supported.go`，build tag
  `//go:build linux || windows`。
- `wgengine/magicsock/sourcepath_linux_test.go` → `sourcepath_supported_test.go`，
  同 build tag。
- `wgengine/magicsock/sourcepath_default.go` build tag 收紧为
  `//go:build !linux && !windows`。

文件改名（不是简单地放宽 build tag）的原因：Go 把 `_linux.go` 文件名后缀视为
**隐式** `//go:build linux` 约束，与文件内显式的 `//go:build linux || windows`
取交集；不重命名 Windows 上不会编译。改用 `_supported.go`，无隐式 GOOS 约束。

注：实施时偏离了本文档 § 2 原写的 `_unix.go` 候选名 —— 因为 `_unix` 在
Tailscale 现有约定里包含 macOS / BSD，本 PR 第一版只覆盖 Linux + Windows，名实
不符；`_supported` 避开了这个问题，未来加 macOS / BSD 也合适。

### W1 验收：Linux 0 行为差异

build tag 改动后 WSL 跑同一命令 → `ok 10.874s`（baseline 11.099s，10.874s，11.457s
三次实测，全部 ±0.5s 内）。Linux 二进制行为无差异。

### W1 验收：Windows 编译 + 测试

Windows host 原生 `go build ./wgengine/magicsock` 通过。原生
`go test ./wgengine/magicsock -count=1` 在引入 IPv6 loopback runtime probe 后
通过 → `ok 10.227s`。

### Windows IPv6 子测试本机受限的发现

未引入 probe-and-skip 之前 4 个 Windows IPv6 子测试 fail：
`TestSendUDPBatchFromSourceAuxDualStackLoopback/ipv6`、
`TestLazyEndpointSendIgnoresForcedAuxDataSourceDualStack/ipv6`、
`TestSourcePathForcedAuxDualNodeRuntime/IPv6`、
`TestSourcePathAutomaticAuxDualNodeRuntime/IPv6`。失败模式统一为
"`read udp6 [::1]:port: i/o timeout`" 或 "direct peer address family mismatch"。

诊断结论（2026-04-29 在 Windows Server 2025 + WSL2 + sing-box + Wintun 环境）：

- `ping -6 ::1` 报 `General failure`，TCP connect `[::1]:port` 报 WSAEACCES。
- IPv4 loopback (`127.0.0.1`) 正常 —— 与 IPv6 loopback 行为完全相反。
- 排查依次验证了 Defender Firewall（关掉无效）、Hyper-V 容器服务
  （`Stop-Service vmcompute` 无效）、WSL2 VM（`wsl --shutdown` 无效）、
  Hyper-V firewall `LoopbackEnabled`（设 True 无效）、自建 WFP helper 在
  ALE_AUTH + INBOUND/OUTBOUND_TRANSPORT V4 + V6 共 8 个 layer 上以 max-weight
  permit `IsLoopback` flag（`netsh wfp show filters` dump 验证 filter 已经
  装入，但 IPv6 loopback 仍 fail）。
- `netsh wfp show filters` 只输出 14 个 layer，所有 provider 都是 Microsoft
  自己的 (`MPSSVC_*` / `IKEEXT` / `TCP_*`)，没有任何第三方 (`sing-box` /
  `wintun` / `Hyper-V`) provider 痕迹 —— 大概率拦截**根本不在 WFP 层**，
  在 NDIS LWF / TDI / Server 2025 自身 hardening 之类的更底层。
- WFP API 是 Windows 安全模型刻意不能从用户态绕过的 —— 当阻断在 WFP 之下时，
  user-mode permit 永远没用。

### W1 处理：Runtime probe-and-skip

引入 `ipv6LoopbackUDPRoundtripProbe()` （`sourcepath_supported_test.go`），
sync.Once 缓存结果，对 `[::1]` 做 500ms UDP roundtrip 实测。在 Windows host 上
的所有 4 个 IPv6 子测试入口都先调 probe，roundtrip 失败时
`t.Skipf("IPv6 loopback UDP roundtrip not delivered on this host (%v); srcsel
IPv6 paths must be validated on a host with working IPv6 loopback or via
real-network tests", err)`。

设计意图：**不是** `runtime.GOOS == "windows"` 黑名单（那会掩盖普通 Win10/11
客户端的真实行为）。probe 是基于实际能力的运行时检测，在 IPv6 loopback 工作
正常的 Windows 机器（CI、客户机、干净 Windows VM）上 probe 通过、IPv6 子测试
正常运行；只在被 sing-box / EDR / Server 2025 hardening 拦掉的机器上跳。

### W1 状态

- W1 通过 by 本机 + 任何 IPv6 loopback 工作的 Windows 环境。
- 4 个 IPv6 子测试在被拦机器上 skip 而不 fail，dev 体验自然。
- Windows IPv6 srcsel 真实行为验证 **deferred 到 W3**，必须在 IPv6 loopback
  工作的机器或真实远端 IPv6 网络上跑。

附属产物：`cmd/srcsel-wfp-loopback-permit/`（含 README.md）—— 投资 1-2 小时做
的 WFP permit 工具，在本机被证明无效（拦截在 WFP 之下），保留为通用工具：
对**其它**用 user-mode WFP filter 拦截的环境（一些 VPN 客户端 / 旧版 sing-box）
仍然有效。

## 9. 一句话执行版

把 PR #1 已 Final Closeout 的 Linux srcsel 通过 build tag 放宽到 Windows，沿
用所有算法与默认值，重点验证 Windows Firewall / Modern Standby / service
mode 三个 Linux 不存在的风险，再做一次 Windows ↔ Linux 双端实网联调，最后
Codex 审计闭环 + Final Closeout。
