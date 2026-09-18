# Codex Route Guard

[English](README.md) · [架构](ARCHITECTURE.md) · [安全模型](SECURITY.md) · [兼容性](COMPATIBILITY.md)

> 面向 Codex 多工具共存场景的配置所有权、路由生命周期与启动竞态协调层。

Codex Route Guard 解决的并不是某两个程序之间的一个临时冲突，而是一类更底层的问题：**多个独立工具同时修改同一份 Codex 配置，却没有统一的字段所有权、事务边界和生命周期协议。**

账号切换器、本地 Responses API bridge、自定义 Router、model catalog 注入器、桌面启动器以及原生 Codex，都可能修改同一个：

```text
~/.codex/config.toml
```

这会形成典型的 **last-writer-wins**：

- OAuth 账号切换成功，但顺手把本地 route 删除；
- 本地 route 服务还在正常运行，但 Codex 已静默退回 native；
- 一个工具写入自己的 model catalog，另一个工具的模型目录随即失效；
- route provider 的 journal 仍认为自己 active，但实际配置已经被其他程序改掉；
- 文件最终被修好了，可 Codex 已经在几毫秒前按旧配置启动并缓存了错误状态；
- 两个 watcher 互相覆盖配置，形成修复循环。

Codex Route Guard 的核心不是“发现变化就全部覆盖回去”，而是：

> **只有能够证明归属的配置才修复；归属不明就 fail closed，绝不抢配置。**

## 核心模型

它把问题拆成三层：

1. **Ownership / 所有权**：这个字段到底归谁管理？
2. **Lifecycle / 生命周期**：route owner 有没有自己的 connect / disconnect 协议？
3. **Reconciliation / 协调修复**：在不破坏未知第三方配置的前提下，哪些状态可以安全恢复？

## 当前 adapter

当前版本的架构可以继续扩展，但实现保持保守：

| 集成 | 当前角色 |
| --- | --- |
| `codex-chatgpt-web` | 第一方 route-owner adapter，读取其 integration journal 并调用 route 生命周期 |
| Cockpit Tools | 已支持的账号/配置变更来源，识别其已知 model catalog |
| Native Codex | native fallback |
| 未知自定义 Router / Provider | 保留，不接管 |
| 其他第三方 route journal | 尚未实现，需要显式 adapter |

所以它不是“万能 Router”。它更像一个 **Codex 控制面协调器**。

## 它具体做什么

### 从权威状态判断 route 所有权

对 `codex-chatgpt-web`，Guard 读取其自己写入的 integration journal。

它不会猜端口，也不会把某个 `127.0.0.1:xxxx` 写死。

### 使用 route owner 自己的生命周期

Web / Native 状态切换优先调用：

```text
route connect
route disconnect
```

这样 journal、恢复状态、hooks、realtime endpoint、cache 等内部状态仍由真正的 owner 管理。

### 只修复已知归属字段

如果账号切换工具误删了当前 Web route，Guard 可以恢复 route。

如果出现的是明确识别为 Cockpit 管理、且会干扰当前 route 的 model catalog，也可以清理。

但下面这些情况不会强行覆盖：

- 未知 `model_provider`
- 未知 `model_catalog_json`
- 与 Web GPT route 不同的自定义 `openai_base_url`

### 处理启动竞态

即使最终配置正确，Codex 也可能已经先启动并缓存了错误 route。

Guard 会把 config 写入时间与“刚启动”的 ChatGPT/Codex Windows packaged process 做关联，只在能够证明是本次启动竞态时，重载对应的同一 app family。

旧会话不会被批量结束。

### 独立运行

Guard 不修改 Cockpit.exe、Codex.exe 或 codex-chatgpt-web 的二进制。

普通更新不会覆盖 Guard。

## 默认自动模式

```text
Web route owner 存活
      ↓
确保 route 已连接
      ↓
外部工具改掉 owned route
      ↓
自动修复
      ↓
若 Codex 刚好抢先启动
      ↓
仅恢复刚启动的匹配 app family

Web route owner 关闭一段时间
      ↓
调用 owner 自己的 route disconnect
      ↓
回到 native Codex
```

## 安装

支持 Windows 10/11。

解压发布包后运行：

```text
INSTALL.cmd
```

或：

```powershell
.\codex-route-guard.exe install
```

不需要管理员权限。

安装位置：

```text
%LOCALAPPDATA%\CodexRouteGuard\
```

如果检测到旧版 **CodexWebGPTGuard**，安装器会停止旧守护进程、移除旧自启动，并迁移原来的模式设置。

## 日常使用

默认 `auto` 模式下，通常不需要直接操作 Guard。

想用 Web 模型：

```text
启动 Codex Web GPT
→ 正常切账号 / 选账号
→ 启动 Codex
```

想用原生 Codex：

```text
关闭 Codex Web GPT
→ 正常切账号 / 选账号
→ 启动 Codex
```

## 状态查看

```powershell
codex-route-guard.exe status
codex-route-guard.exe version
```

日志：

```text
%LOCALAPPDATA%\CodexRouteGuard\guard.log
```

## 安全原则

- 只接受 HTTP loopback `/v1` route；
- launcher descriptor 必须匹配预期 kind 和本机 endpoint；
- 未知 provider / catalog / custom route 不覆盖；
- 配置采用原子写入；
- 单用户会话只运行一个 Guard；
- 启动竞态恢复有严格时间窗口；
- 不会故意结束已有旧 Codex/ChatGPT 会话；
- 能走 owner 生命周期时，不用粗暴字段覆盖代替。

## 项目方向

长期目标不是给某个程序打补丁，而是形成：

```text
Codex Route Guard
  ├─ route owner adapters
  ├─ account/profile mutation adapters
  ├─ model catalog ownership rules
  ├─ conflict reporting
  └─ reconciliation policy
```

任何新 adapter 都必须遵守同一条原则：

**已知所有权可以协调；未知所有权必须保留。**
