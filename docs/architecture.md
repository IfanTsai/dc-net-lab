# DCNetLab 架构

本文描述平台的整体架构：控制面与数据面的进程级分离、各组件的职责与通信契约、单机与多机两种部署形态。网络模型与资源设计见 [design.md](design.md)。

## 1. 总览

平台由三类进程组成，任意两者之间都只通过网络协议通信，不共享文件系统：

```text
                    浏览器
                      │ HTTP/WebSocket
                      ▼
              ┌───────────────┐
              │  web (:8080)  │  静态托管 Vue 构建产物
              │               │  反代 /api /ws /metrics
              └───────┬───────┘
                      │ HTTP
                      ▼
              ┌───────────────────────────┐
              │  controller (:8180 API)   │  控制面
              │  - protobuf API (Kratos)  │
              │  - SQLite + Operation     │
              │  - 编译器(FRR/clab 产物)  │
              │  - 包制品库 (:50062)      │
              └───────┬───────────────────┘
                      │ gRPC (Agent API)
                      ▼
              ┌───────────────────────────┐
              │  agent (:50063)           │  数据面
              │  - containerlab deploy    │
              │  - docker exec/pause/...  │
              │  - 管理网拨号代理         │
              │  - 包制品库反代 (:50062)  │
              └───────┬───────────────────┘
                      │ docker / containerlab CLI
                      ▼
        ┌─────────────────────────────────────┐
        │  实验容器(交换机 / server / edge)   │
        │  server 内: node-agent (:50061)     │
        └─────────────────────────────────────┘
```

单机部署时三个进程跑在同一台机器上、经 localhost 通信；多机部署时把 agent 放到专用宿主机，链路完全不变。

## 2. 组件职责

### 2.1 controller（控制面）

Go 模块化单体（Kratos 框架），分层严格单向 service → biz → data：

- 资源模型（Lab/Node/Link/Plan/Program/Package/FaultScenario/CaptureSession 等）持久化于 SQLite，protobuf 为 API 唯一事实来源；
- Plan/Apply 声明式变更：编译 FRR 配置与 Containerlab 拓扑（纯内存、可重放）、保存 Generation 快照、经 Runtime Driver 部署并校验控制面/数据面收敛；
- 包制品库（:50062，HTTP）：tar.gz 制品的存储与分发源，内置 trafficgen 与 capture 两个 builtin 包；
- 完全不接触 docker/containerlab——所有运行时操作经 `runtime.Driver` 接口出去。

`runtime.Driver` 有两个实现：`agentdriver`（gRPC 客户端，拨向 agent，默认）与 `noop`（只产出物不部署，无 Docker 环境下的降级）。接口语义（stdin-EOF 停止信号、`ErrUnavailable` 快速失败分类等）在两端保持一致。

### 2.2 agent（数据面）

跑在容器宿主机上的 daemon，controller 是它唯一的客户端，服务 Agent gRPC API（`api/agent/v1`）：

- **Deploy/Destroy**：RPC 随请求携带整套 generation 产物文件（拓扑 YAML + 每节点 FRR 配置），agent 落盘到自己的数据目录后执行 `containerlab deploy/destroy`，同一 key 幂等；
- **Exec / ExecStream / Terminal**：容器内命令执行。ExecStream 承载抓包（客户端半关流 = 容器内工具的 stdin EOF 停止信号，连接断裂等价，容器内不留孤儿进程）；Terminal 承载 Web 终端（stdin 字节 + resize 事件双向流）；
- **故障与电源**：pause/unpause、`ip link set`、`tc netem` 损伤的注入与恢复；
- **Dial（拨号代理）**：把一条 TCP 连接中转进容器管理网。controller 对 node-agent 的所有 gRPC 调用经此转发，因此跨机部署不要求 controller 能路由到数据面机器上的 docker 管理网桥；
- **包制品库反代**（可选，`--repo-upstream`）：容器经管理网关拉包时到达的是宿主机地址，多机部署时由 agent 反代回源到 controller 的 :50062。单机时 controller 直接应答，无需开启。

错误分类跨网络保留：运行时本身不可用（docker daemon 挂了、agent 不可达）映射为 gRPC `Unavailable` → 客户端还原成 `runtime.ErrUnavailable`，调用方据此快速失败而非重试到超时。

### 2.3 web（UI 层）

轻量 Go 静态服务（`web/server`）：托管 Vue 3 构建产物，把 `/api`、`/ws`、`/metrics` 反代到 controller。浏览器只面对一个源，前端全部使用相对路径，任何机器访问均可用、无 CORS。开发模式下改为反代 Vite dev server（HMR WebSocket 透传），入口 URL 不变。

### 2.4 容器内组件

- **node-agent**（gRPC :50061）：server 容器内的进程监督 daemon，烤入 `dcnetlab/server` 镜像。管理软件包安装（从制品库拉取、校验、多版本共存、按 spec links 建软链）与程序生命周期（systemd 语义），无任意命令接口。controller 经 agent 拨号代理驱动它。
- **capture**：AF_PACKET 抓包工具（纯 Go 静态二进制）。交换机是主要抓包目标，`dcnetlab/frr` 镜像直接预装；server 镜像不带，Apply 的 `InstallCaptureTool` 步骤经包制品库下发安装，links 机制把它软链到与交换机一致的路径。两条分发路径最终提供相同的 `/opt/dcnetlab/bin/capture` 调用契约。
- **trafficgen**：内置流量应用，始终以 builtin 包形式经制品库分发。

## 3. 节点镜像

所有实验节点镜像由 `make images` 构建（Dockerfile 在 `build/` 下）：

| 镜像 | 基础 | 附加内容 | 用途 |
|---|---|---|---|
| `dcnetlab/frr:10.2.1` | quay.io/frrouting/frr:10.2.1 | capture 工具 | 交换机/路由器 |
| `dcnetlab/server:10.2.1` | quay.io/frrouting/frr:10.2.1 | node-agent、node-cli | server 节点 |
| `dcnetlab/frr-edge:10.2.1` | dcnetlab/frr:10.2.1 | iptables | dcedge/external（外网访问） |

镜像 tag 跟随基础 FRR 版本，必须与 `containerlab.DefaultOptions` 一致；Apply 的 `EnsureImages` 步骤在部署前校验镜像存在。镜像内烤的是"引导版本"——node-agent 与容器内工具的热升级仍走包制品库，镜像不必频繁重建。

## 4. 端口与进程清单

| 组件 | 端口 | 协议 | 默认绑定 | 说明 |
|---|---|---|---|---|
| web | 8080 | HTTP | 127.0.0.1 | 用户入口（UI + API 反代） |
| controller | 8180 | HTTP | 127.0.0.1 | protobuf REST API |
| controller | 9090 | gRPC | 127.0.0.1 | protobuf gRPC API |
| controller | 50062 | HTTP | 0.0.0.0 | 包制品库（容器经管理网关拉取） |
| agent | 50063 | gRPC | 127.0.0.1 | Agent API（多机时绑定可路由地址） |
| agent | 50062 | TCP | 0.0.0.0 | 包制品库反代（仅 `--repo-upstream` 时开启） |
| node-agent | 50061 | gRPC | 容器内 | controller 经拨号代理到达 |
| node-agent | 9100 | HTTP | 容器内 | Prometheus /metrics |

## 5. 部署形态

### 5.1 单机（默认）

`make up` 依次构建二进制与节点镜像，然后拉起 agent → controller → web 三个进程，全部经 localhost 通信。`make dev` 额外拉起 Vite 并让 web 反代它。`make down` 统一回收。macOS 上整个流程自动转发进 OrbStack 机器执行（见 [macos.md](macos.md)）。

### 5.2 控制面 / 数据面分机

数据面机器只需 docker + containerlab + 一个 `dcnetlab-agent` 二进制（首次带外安装，如 scp + systemd unit）：

```bash
# 数据面机器
dcnetlab-agent --listen 0.0.0.0:50063 --repo-upstream <controller-ip>:50062

# 控制面机器
dcnetlab-controller --listen 127.0.0.1:8180 --agent <agent-ip>:50063

# UI(通常与控制面同机)
dcnetlab-web --listen 0.0.0.0:8080 --controller http://127.0.0.1:8180 --web-dir web/dist
```

跨机链路上的三类流量与解法：

1. **运行时操作**（deploy/exec/故障）：本来就是 Agent gRPC，天然跨机；
2. **controller → node-agent**：容器管理网只存在于数据面机器，经 agent 的 Dial 双向流代理；
3. **容器 → 包制品库**：容器拉包的目标是其管理网关（数据面机器上的地址），由 agent 的 repo 反代回源到 controller。

### 5.3 安全边界

所有 gRPC/HTTP 通道均为明文、无认证，与平台"本地实验工具"的定位一致；多机部署时 agent 与 controller 之间应处于可信内网（或自行叠加 WireGuard 等隧道）。node-agent 与 agent 均无任意命令执行接口之外的最小面（node-agent 完全没有，agent 的 exec 面向实验容器）。

## 6. 代码结构约束

- 单 Go module，顶层按组件分目录：`controller/`（控制面）、`agent/`（数据面 agent + clab 驱动）、`nodeapps/`（交付进仿真容器的应用）、`web/server`（UI 托管）；
- `controller/internal`、`agent/internal` 与 `nodeapps/internal` 由 Go internal 可见性规则强制互不可见；
- 跨面共享只有三种载体：`api/`（proto 契约）、`pb/`（生成代码）、根 `internal/`（`model` 资源模型、`nodeagentapi` 线上常量、`runtime` Driver 接口）；
- 每层同名文件持有该层 wire ProviderSet 的约定在 `controller/internal` 内不变。
