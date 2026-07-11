# DCNetLab 数据中心网络模拟平台技术设计文档

## 1. 文档信息

| 项目    | 内容                                                |
| ----- | ------------------------------------------------- |
| 项目名称  | DCNetLab                                          |
| 文档类型  | 总体技术设计文档                                          |
| 文档版本  | V1.0                                              |
| 目标平台  | Linux、macOS                                       |
| 后端技术栈 | Golang、Kratos                                             |
| 前端技术栈 | Vue 3、TypeScript                                  |
| 网络运行时 | Docker、Containerlab、FRRouting、Linux Network Stack |
| 数据存储  | SQLite                                            |
| 设计目标  | 快速实现物理网络闭环，并支持持续扩展虚拟网络、业务程序和 Server Daemon        |

## 2. 项目定位

DCNetLab 是一套运行在单台开发机上的数据中心网络模拟平台。平台使用真实 Linux 网络栈、真实 BGP 控制平面和真实应用程序，模拟一个规模较小但层次完整的数据中心网络。

系统通过 Web UI 提供以下能力：

* 创建和部署数据中心物理拓扑。
* 查看设备、接口、BGP、路由和链路状态。
* 在拓扑中增加 Pod、Rack、Leaf 和 Server。
* 在 Server 上运行真实业务程序。
* 生成 HTTP、TCP、UDP、DNS 等真实流量。
* 在任意节点和接口上通过 UI 抓包。
* 注入链路、设备、延迟、丢包和限速故障。
* 观察路由收敛和业务影响。
* 后续扩展 VPC、Subnet、VRF、VXLAN、EIP 和 BGP EVPN。
* 后续在 Server 上运行 Pingmesh、Host Observer、日志采集等常驻 Daemon。

Containerlab 负责根据节点和链路定义创建容器化网络实验拓扑，FRRouting 负责 BGP、BFD 和后续 EVPN 等控制平面。Containerlab 的核心模型本身也是节点和链路，适合作为第一阶段 Runtime Backend；但系统自己的资源模型仍是唯一事实来源，Containerlab 拓扑文件仅作为编译产物。

## 3. 设计目标

### 3.1 核心目标

第一阶段必须形成以下完整闭环：

```text
创建数据中心拓扑
        ↓
自动分配 IP 和 ASN
        ↓
部署 FRR 路由节点和物理链路
        ↓
建立 eBGP Clos Fabric
        ↓
在 Server 上运行真实程序
        ↓
生成持续业务流量
        ↓
通过 UI 查看路由、流量和报文
        ↓
注入网络故障
        ↓
观察控制平面和业务影响
```

系统设计应满足：

1. Linux 和 macOS 上能够快速启动。
2. 用户的主要操作均通过 Web UI 完成。
3. 后端统一使用 Golang 实现。
4. 前端统一使用 Vue 3 和 TypeScript 实现。
5. 网络设备使用真实 FRR 和 Linux Kernel FIB。
6. Server 不只是拓扑终点，而是可以运行应用、Daemon 和持久化数据。
7. 抓包能力由平台内建，不要求用户手工运行 tcpdump 或 Wireshark。
8. 资源、配置和运行状态分离。
9. 第一版使用模块化单体，控制系统复杂度。
10. 后续扩展虚拟网络时不重构核心资源模型。

### 3.2 非目标

第一阶段不实现：

* ASIC Pipeline 精确模拟。
* 交换芯片 Buffer 和硬件 ECMP Hash 精确模拟。
* 真实线速吞吐。
* 厂商 NOS 完整模拟。
* 数千节点单机运行。
* Kubernetes 作为平台前置依赖。
* 多 Controller 高可用。
* 分布式事务和复杂多租户权限体系。
* Wireshark 全协议解析能力。
* 完整虚拟机 Hypervisor。

## 4. 核心设计原则

### 4.1 声明式状态

用户通过 API 或 UI 修改 Desired State。Controller 比较 Desired State 与 Observed State，生成 Plan 并驱动运行时收敛。

```text
User Intent
    ↓
Desired State
    ↓
Validator
    ↓
Planner
    ↓
Execution Plan
    ↓
Reconciler
    ↓
Runtime Driver
    ↓
Linux / Docker / FRR
    ↓
Observer
    ↓
Observed State
```

UI 不得直接操作 Docker、Containerlab、FRR 或宿主机网络。

### 4.2 资源模型是唯一事实来源

以下内容不是平台主数据：

* Containerlab YAML。
* FRR 配置文件。
* Docker Container Label。
* Linux 当前接口状态。
* nftables 当前规则。
* 运行中的进程列表。

这些内容分别属于：

```text
Runtime Artifact
Observed State
```

系统真正的主数据是：

```text
Lab
IDC
Pod
Rack
Node
Link
Server
Program
Daemon
Workload
Traffic Scenario
Capture Session
Fault Scenario
VPC
Subnet
EIP
```

### 4.3 模块化单体优先

第一版后端采用一个 Golang 模块化单体进程：

```text
dcnetlab-controller
```

内部按领域拆分 package，不在初期引入微服务。

独立进程仅保留：

```text
dcnetlab-controller
dcnetlab-agent
dcnetlab-server-agent
dcnetlab-lab-app
Vue Web UI
FRR Containers
Server Containers
Workload Containers
```

### 4.4 API 优先

每项能力必须先定义 API 和领域接口，再实现 UI。

CLI 仅用于：

* 平台启动和停止。
* 开发调试。
* CI 自动化。
* 故障诊断包导出。
* 必要的灾难恢复。

正常实验流程不得依赖 CLI。

### 4.5 第一版全量部署，后续增量收敛

第一版允许拓扑变更后重新生成并部署完整 Containerlab 拓扑，以降低增量链路和容器管理的复杂度。

必须确保：

* Desired State 不丢失。
* Persistent Volume 不丢失。
* Program 和 Daemon 定义不丢失。
* Apply 前可以查看 Plan。
* Apply 失败可以恢复上一 Generation。
* 网络拓扑生命周期与业务数据生命周期解耦。

## 5. 总体架构

```text
┌───────────────────────────────────────────────────────────────┐
│                         Web UI                                │
│              Vue 3 + TypeScript + Vite                       │
│                                                               │
│ Labs │ Topology │ Nodes │ Servers │ Programs │ Traffic       │
│ Capture │ Faults │ Operations │ Virtual Network              │
└──────────────────────────────┬────────────────────────────────┘
                               │ REST / WebSocket
┌──────────────────────────────▼────────────────────────────────┐
│                    DCNetLab Controller                        │
│                         Golang                                │
│                                                               │
│ API Server            Resource Store       Operation Manager │
│ Topology Service      Planner              Reconciler        │
│ Allocator             Artifact Compiler    Runtime Manager   │
│ Observer              Server Manager       Program Manager   │
│ Daemon Manager        Traffic Manager      Capture Manager   │
│ Fault Manager         Virtual Network Manager                │
└─────────────┬────────────────────┬────────────────────────────┘
              │ gRPC               │ Docker Engine API
┌─────────────▼────────────┐   ┌───▼───────────────────────────┐
│ Runtime Agent           │   │ Container Runtime             │
│                         │   │                               │
│ Netlink                 │   │ FRR Nodes                     │
│ Namespace               │   │ Compute Server Containers     │
│ AF_PACKET               │   │ Workload Containers           │
│ qdisc                   │   │ External Service Containers   │
│ nftables                │   │                               │
└─────────────┬────────────┘   └───────────┬───────────────────┘
              │                            │
┌─────────────▼────────────────────────────▼───────────────────┐
│                    Linux Network Stack                       │
│ veth │ Bridge │ Route │ VRF │ VXLAN │ nftables │ conntrack │
└──────────────────────────────────────────────────────────────┘
```

Docker Engine 提供 REST API 和 Go SDK，Controller 可以通过 API 管理容器、镜像、Volume、日志和生命周期，不需要依赖用户执行 Docker CLI。Go SDK 支持与 Docker Engine 协商 API 版本，有利于兼容不同开发机环境。

## 6. 数据中心物理网络设计

### 6.1 设备角色

系统采用功能角色命名，不绑定特定厂商型号。

| 角色              | 说明                                   |
| --------------- | ------------------------------------ |
| External Router | 模拟 ISP、骨干网、SBB 或其他 IDC               |
| DC Edge Gateway | 数据中心物理出口，功能上类似由 ASR、MX、NCS 等承载的 Edge |
| SuperSpine      | IDC 内连接多个 Pod 和 DC Edge              |
| Spine           | Pod 内连接所有 Leaf                       |
| Leaf            | 连接 Rack 和 Server                     |
| Server          | 运行应用程序、Daemon 和虚拟网络数据面               |
| EGW             | 后续用于 VPC EIP、SNAT 和 DNAT             |

默认拓扑：

```text
                     External Router
                       /          \
                DC-Edge-1      DC-Edge-2
                    |   \        /   |
                SuperSpine-1  SuperSpine-2
                     |             |
              ┌──────┴──────┬──────┴──────┐
              │             │             │
            Pod-1         Pod-2         ...
              │
         Spine-1 ───── Spine-2
           /  \          /  \
       Leaf-1 Leaf-2  Leaf-1 Leaf-2
         |      |       |      |
      Server Server  Server  Server
```

Micro Profile 可以只启用：

```text
1 External Router
1 DC Edge Gateway
1 SuperSpine
2 Spine
2 Leaf
4 Server
```

Standard Profile 使用双设备冗余和双 Pod。

### 6.2 Underlay

第一阶段采用纯三层 eBGP Clos：

* 网络节点之间使用三层点到点链路。
* 每条链路分配独立 `/31`。
* 每台设备具有 `/32` Loopback。
* 每台网络设备使用独立 ASN。
* Leaf、Spine、SuperSpine 和 DC Edge 均运行 FRR。
* Server 运行 FRR 与机柜 Leaf 对建 eBGP；静态默认路由指向 VRRP 虚拟网关（主用），BGP 学到的默认路由为热备。
* Linux Kernel FIB 承担实际数据转发。
* ECMP 使用 Linux Multipath Route。
* BFD 作为第二阶段增强能力。

FRRouting 是面向 Linux 和 Unix 系统的路由协议套件，支持 BGP、BFD 等协议，并提供后续 EVPN 控制平面能力，因此适合同时覆盖物理 Underlay 和后续虚拟网络实验。

### 6.3 地址池

内网统一从 10.0.0.0/8 划分：基础设施池各占下半段的一个 /16 块（10.4–10.99、10.101–10.127 预留给未来的池），上半段 10.128.0.0/9 整体保留给 Overlay 工作负载；public 池刻意位于内网段之外（RFC 2544 基准测试地址段），用于模拟公网。

```yaml
addressPools:
  fabricP2P:
    cidr: 10.0.0.0/16
    allocationPrefix: 31

  loopback:
    cidr: 10.1.0.0/16
    allocationPrefix: 32

  vtep:
    cidr: 10.2.0.0/16
    allocationPrefix: 32

  management:
    cidr: 10.3.0.0/16
    allocationPrefix: 32

  serverVlan:            # 每机柜一个 /24：10.100.<rack>.0/24
    cidr: 10.100.0.0/16  # .1 = VRRP 虚网关，.2/.3 = leaf vlanif，.11+ = server
    allocationPrefix: 24

  workload:
    cidr: 10.128.0.0/9
    allocationPrefix: 24

  public:
    cidr: 198.18.0.0/15
    allocationPrefix: 32
```

### 6.4 ASN 规划

| 角色              |       ASN 范围 |
| --------------- | -----------: |
| External Router |  64500–64599 |
| DC Edge Gateway |  64600–64699 |
| SuperSpine      |  65100–65199 |
| Spine           |  65200–65999 |
| Leaf            | 4200080000 + 全局机柜序号（MLAG 对共享） |
| Server          | 固定 65000（well-known） |

Server 使用固定的 well-known ASN 65000，因此 DC Edge 段从 65000 挪到 64600 起，避免 AS-Path 冲突；Leaf ASN 按机柜计算（机柜编号跨 Pod 全局递增），不走范围分配器。

地址和 ASN 必须通过 Allocator 分配，模板中不得计算资源。

## 7. 后端技术设计

### 7.1 Golang 技术栈

后端统一使用 Golang。

推荐组件：

| 能力                | 技术                                         |
| ----------------- | ------------------------------------------ |
| HTTP API          | Go 标准库 `net/http` 或轻量 Router               |
| WebSocket         | Gorilla WebSocket 或等价实现                    |
| 内部 RPC            | gRPC + Protobuf                            |
| 数据库               | SQLite                                     |
| SQL 访问            | `database/sql` + 显式 Repository             |
| 容器管理              | Docker Go SDK                              |
| Linux 网络          | Netlink                                    |
| Network Namespace | `setns` 封装                                 |
| 抓包                | AF_PACKET                                  |
| 配置生成              | `text/template`                            |
| 日志                | `log/slog`                                 |
| 配置                | YAML + 环境变量                                |
| 指标                | Prometheus 格式 Exporter                     |
| 测试                | Go Test、Testcontainers 或本地 Runtime Fixture |

第一版不建议使用重量级 Web Framework 和复杂 ORM。核心资源状态和事务行为应通过显式 Service、Repository 和 Transaction 管理。

### 7.2 后端模块

```text
internal/
├── api
├── auth
├── model
├── store
├── operation
├── topology
├── planner
├── allocator
├── reconciler
├── compiler
├── runtime
├── observer
├── server
├── program
├── daemon
├── workload
├── traffic
├── capture
├── fault
├── virtualnetwork
└── audit
```

模块依赖方向：

```text
API
 ↓
Application Service
 ↓
Domain Model
 ↓
Repository / Runtime Interface
 ↓
SQLite / Docker / Agent / Containerlab
```

禁止：

* API Handler 直接调用 Docker。
* Store 层调用 Runtime。
* Vue 前端感知 Docker Container ID。
* Runtime Driver 修改 Desired State。
* Observer 直接决定资源变更。

### 7.3 分层职责

#### API Layer

负责：

* 请求解析。
* 参数基础校验。
* 身份和权限校验。
* 幂等键处理。
* 错误映射。
* 返回 Operation ID。

#### Application Service

负责：

* 用例编排。
* 事务边界。
* 调用 Planner。
* 创建 Operation。
* 触发 Reconcile。
* 聚合查询结果。

#### Domain Layer

负责：

* 资源模型。
* 状态机。
* 业务约束。
* 资源关系。
* Plan 差异语义。

#### Infrastructure Layer

负责：

* SQLite。
* Docker Engine。
* Containerlab。
* FRR。
* Linux Netlink。
* AF_PACKET。
* 文件系统。

## 8. 前端技术设计

### 8.1 Vue 3 技术栈

前端统一采用：

| 能力    | 技术                                 |
| ----- | ---------------------------------- |
| 框架    | Vue 3                              |
| 语言    | TypeScript                         |
| 编译工具  | Vite                               |
| 组件模式  | Composition API + `<script setup>` |
| 状态管理  | Pinia                              |
| 路由    | Vue Router                         |
| UI 组件 | Element Plus                       |
| 网络拓扑  | Cytoscape.js                       |
| 时序图表  | Apache ECharts                     |
| HTTP  | Axios 或统一 Fetch Client             |
| 实时通信  | WebSocket                          |
| 单元测试  | Vitest                             |
| 组件测试  | Vue Test Utils                     |
| E2E   | Playwright                         |

Vue 官方文档推荐 Vue 3 TypeScript 项目使用 Vite，并支持在 Composition API 和 `<script setup>` 中进行类型声明；Pinia 用于跨组件和跨页面共享状态。

Cytoscape.js 用于显示和操作交互式图结构，支持节点、边、事件和图布局，适合作为数据中心拓扑的渲染基础。

### 8.2 页面结构

```text
Dashboard
Labs
Topology
Nodes
Servers
Programs
Daemons
Workloads
Traffic
Capture
Faults
Operations
Virtual Networks
Settings
```

第一阶段优先完成：

```text
Labs
Topology
Node Detail
Server Detail
Programs
Traffic
Capture
Faults
Operations
```

### 8.3 前端目录结构

```text
web/
├── src/
│   ├── api/
│   │   ├── client.ts
│   │   ├── lab.ts
│   │   ├── topology.ts
│   │   ├── node.ts
│   │   ├── server.ts
│   │   ├── program.ts
│   │   ├── daemon.ts
│   │   ├── traffic.ts
│   │   ├── capture.ts
│   │   ├── fault.ts
│   │   └── operation.ts
│   ├── components/
│   │   ├── common/
│   │   ├── topology/
│   │   ├── server/
│   │   ├── program/
│   │   ├── traffic/
│   │   └── capture/
│   ├── composables/
│   │   ├── useWebSocket.ts
│   │   ├── useOperation.ts
│   │   ├── useTopology.ts
│   │   ├── usePacketStream.ts
│   │   └── useTrafficMetrics.ts
│   ├── layouts/
│   ├── pages/
│   ├── router/
│   ├── stores/
│   ├── types/
│   ├── utils/
│   ├── App.vue
│   └── main.ts
├── vite.config.ts
├── tsconfig.json
└── package.json
```

### 8.4 Pinia Store

按领域拆分：

```text
useLabStore
useTopologyStore
useNodeStore
useServerStore
useProgramStore
useDaemonStore
useTrafficStore
useCaptureStore
useFaultStore
useOperationStore
```

Store 仅保存跨页面共享状态。表单临时数据、Drawer 状态和组件交互状态保留在组件内部。

### 8.5 拓扑页面

```text
TopologyPage.vue
├── TopologyToolbar.vue
├── TopologyCanvas.vue
├── NodeDetailDrawer.vue
├── LinkDetailDrawer.vue
├── TopologyContextMenu.vue
├── FlowPathPanel.vue
└── OperationProgressPanel.vue
```

`TopologyCanvas.vue` 只负责：

* 初始化 Cytoscape 实例。
* 渲染节点和链路。
* 应用分层布局。
* 更新状态样式。
* 处理选择、缩放和定位。
* 向外抛出节点和链路事件。

抓包、故障、扩容等业务逻辑由页面 Service 和 Store 处理，不直接写入 Cytoscape 事件代码。

## 9. 核心资源模型

### 9.1 公共元数据

```go
type ResourceMeta struct {
    ID                 string
    Name               string
    Generation         int64
    ObservedGeneration int64
    Phase              ResourcePhase
    LastError          *ResourceError
    CreatedAt          time.Time
    UpdatedAt          time.Time
}
```

统一 Phase：

```text
Pending
Planning
Applying
Running
Degraded
Failed
Stopping
Stopped
Deleting
Deleted
```

### 9.2 Lab

```go
type Lab struct {
    Meta ResourceMeta
    Spec LabSpec
    Status LabStatus
}

type LabSpec struct {
    Profile      string
    IDCCount     int
    AddressPools []AddressPool
    ASNPool      ASNPool
}
```

### 9.3 Node

```go
type Node struct {
    Meta ResourceMeta
    Spec NodeSpec
    Status NodeStatus
}

type NodeSpec struct {
    LabID       string
    Role        NodeRole
    IDCID       string
    PodID       string
    RackID      string
    ASN         uint32
    Loopback    netip.Prefix
    RuntimeType RuntimeType
}
```

### 9.4 Link

```go
type Link struct {
    Meta ResourceMeta
    Spec LinkSpec
    Status LinkStatus
}

type LinkSpec struct {
    LabID     string
    EndpointA LinkEndpoint
    EndpointB LinkEndpoint
    MTU       int
}
```

### 9.5 Server

```go
type Server struct {
    Meta ResourceMeta
    Spec ServerSpec
    Status ServerStatus
}

type ServerSpec struct {
    NodeID       string
    RackID       string
    Uplink       ServerUplink
    VTEP         *netip.Addr
    Capacity     ResourceCapacity
    Labels       map[string]string
    ProgramPolicy ProgramPolicy
}
```

### 9.6 Program

Program 表示运行在 Server 上的可执行软件实例。

```go
type Program struct {
    Meta ResourceMeta
    Spec ProgramSpec
    Status ProgramStatus
}

type ProgramSpec struct {
    ServerID     string
    Kind         ProgramKind
    Package      ProgramPackage
    Command      []string
    Arguments    []string
    Environment  map[string]string
    NetworkMode  ProgramNetworkMode
    RestartPolicy RestartPolicy
    HealthCheck  *HealthCheckSpec
    Resources    ProgramResources
    Volumes      []VolumeMount
}
```

`ProgramKind`：

```text
Application
Daemon
Probe
TrafficGenerator
Utility
```

### 9.7 Daemon

Daemon 是 Program 的受控子类型，不需要建立完全独立的执行体系。

```go
type DaemonSpec struct {
    ProgramSpec

    StartupOrder     int
    Required         bool
    AutoStart        bool
    RestartBackoff   time.Duration
    MaxRestartCount  int
    ReadinessCheck   *HealthCheckSpec
    LivenessCheck    *HealthCheckSpec
    ConfigTemplateID string
}
```

未来的 Pingmesh、日志 Agent、Host Metrics Agent、BGP Observer 等都使用 Daemon 模型。

### 9.8 Workload

Workload 表示具有独立网络身份的应用实例。

```go
type Workload struct {
    Meta ResourceMeta
    Spec WorkloadSpec
    Status WorkloadStatus
}

type WorkloadSpec struct {
    ServerID    string
    Image       string
    Command     []string
    Network     WorkloadNetwork
    Storage     []VolumeMount
    Resources   WorkloadResources
}
```

Program 和 Workload 的区别：

| 对象       | 运行位置                          | 网络身份         | 典型用途                     |
| -------- | ----------------------------- | ------------ | ------------------------ |
| Program  | Server 自身或共享 Server Namespace | 使用 Server IP | Pingmesh、Host Agent、基础服务 |
| Daemon   | Server 自身                     | 使用 Server IP | 常驻探测和采集进程                |
| Workload | 独立 Container Namespace        | 独立 IP        | VM、容器业务、VPC Endpoint     |

## 10. Server 运行时设计

### 10.1 Server 表示方式

每台 Server 由一个长生命周期的 Compute Server Container 表示。

Server Container 内包含：

```text
dcnetlab-server-agent
Server Network Namespace
Uplink Interface
Loopback
Bridge
Optional VRF
Optional VXLAN
Program Runtime Directory
Daemon Runtime Directory
Log Directory
```

Server 通过 veth 与 Leaf 相连。

### 10.2 Server 上的两类程序

#### Host Program

直接运行在 Server Network Namespace 中，共享 Server 的 IP、路由和接口。

适合：

* Pingmesh Agent。
* Host Network Observer。
* HTTP 测试服务。
* DNS Client。
* 日志采集程序。
* 主机路由探测程序。

#### Isolated Workload

运行在独立 OCI Container 中，具有独立 Network Namespace 和独立 IP。

适合：

* 模拟 VM。
* VPC Workload。
* 数据库。
* 多租户应用。
* EIP 绑定对象。

### 10.3 Program 包格式

平台支持两种 Program Package：

```text
OCI Image
Native Binary Bundle
```

第一阶段优先支持 OCI Image，因为其依赖、文件系统和版本更容易管理。

Native Binary Bundle 用于后续部署 Pingmesh 等单二进制程序：

```yaml
program:
  name: pingmesh-agent
  kind: daemon
  package:
    type: binary
    artifact: pingmesh-agent-linux-amd64
    version: 1.0.0
    checksum: sha256:...
  command:
    - /opt/dcnetlab/programs/pingmesh-agent/pingmesh-agent
  args:
    - --config
    - /etc/dcnetlab/pingmesh/config.yaml
```

### 10.4 Server Agent

每台 Server 内运行 `dcnetlab-server-agent`。

职责：

* 安装 Program Package。
* 生成配置文件。
* 启动和停止程序。
* 维护 PID 和进程状态。
* 执行健康检查。
* 收集 stdout 和 stderr。
* 执行 Restart Policy。
* 上报资源使用情况。
* 管理程序日志轮转。
* 管理 Program Volume。
* 提供受控的程序诊断接口。

Server Agent 不提供任意 Shell 执行 API。

### 10.5 Program 生命周期

```text
Pending
   ↓
Installing
   ↓
Configured
   ↓
Starting
   ↓
Running
   ↓
Stopping
   ↓
Stopped
```

异常状态：

```text
InstallFailed
StartFailed
Unhealthy
CrashLoop
Unknown
```

### 10.6 Restart Policy

```text
Never
OnFailure
Always
UnlessStopped
```

Daemon 默认：

```text
RestartPolicy = Always
AutoStart = true
```

普通测试程序默认：

```text
RestartPolicy = OnFailure
```

### 10.7 健康检查

支持：

```text
Process
TCP
HTTP
Command Adapter
Heartbeat
```

为了避免开放任意命令，Command Check 必须引用预注册的 Adapter，不能接收任意 Shell 字符串。

示例：

```yaml
healthCheck:
  type: http
  address: 127.0.0.1
  port: 9090
  path: /health
  interval: 5s
  timeout: 1s
  failureThreshold: 3
```

### 10.8 Daemon 依赖关系

Daemon 支持简单依赖：

```yaml
daemon:
  name: pingmesh-agent
  dependsOn:
    - server-network-ready
    - topology-discovery-ready
```

第一版只支持有向无环依赖，不实现复杂工作流引擎。

启动顺序：

```text
Server Network Ready
        ↓
Server Agent Ready
        ↓
Required Daemons
        ↓
Optional Daemons
        ↓
Applications
```

### 10.9 Pingmesh 扩展方式

后续接入 Pingmesh 时，建议拆为：

```text
Pingmesh Controller Adapter
Pingmesh Server Daemon
Pingmesh Target Discovery
Pingmesh Result Collector
Pingmesh Visualization
```

平台资源关系：

```text
Topology / Server Inventory
           ↓
Pingmesh Target Planner
           ↓
Probe Configuration
           ↓
Pingmesh Daemon
           ↓
Probe Result
           ↓
DCNetLab Metrics API
           ↓
Vue 3 Visualization
```

Pingmesh 不应直接读取 SQLite，也不应自行发现 Docker Container。它只通过稳定的 Daemon 配置和 Result API 与平台集成。

建议的 Daemon Provider 接口：

```go
type DaemonProvider interface {
    Type() string

    Validate(
        ctx context.Context,
        spec DaemonSpec,
    ) error

    RenderConfig(
        ctx context.Context,
        server Server,
        daemon DaemonSpec,
    ) ([]RenderedFile, error)

    BuildLaunchSpec(
        ctx context.Context,
        server Server,
        daemon DaemonSpec,
    ) (LaunchSpec, error)

    ParseStatus(
        ctx context.Context,
        raw AgentProgramStatus,
    ) (ProgramStatus, error)
}
```

第一阶段实现：

```text
GenericDaemonProvider
```

后续增加：

```text
PingmeshDaemonProvider
NodeExporterDaemonProvider
CustomProbeDaemonProvider
```

## 11. Runtime 设计

### 11.1 Runtime Driver

```go
type RuntimeDriver interface {
    DeployLab(
        ctx context.Context,
        artifact LabArtifact,
    ) error

    DestroyLab(
        ctx context.Context,
        labID string,
    ) error

    StartNode(
        ctx context.Context,
        nodeID string,
    ) error

    StopNode(
        ctx context.Context,
        nodeID string,
    ) error

    RestartNode(
        ctx context.Context,
        nodeID string,
    ) error

    InspectNode(
        ctx context.Context,
        nodeID string,
    ) (RuntimeNode, error)
}
```

第一阶段实现：

```text
ContainerlabRuntimeDriver
```

后续支持：

```text
NativeNamespaceRuntimeDriver
RemoteLinuxRuntimeDriver
KubernetesRuntimeDriver
```

### 11.2 Artifact Compiler

```go
type ArtifactCompiler interface {
    Compile(
        ctx context.Context,
        state DesiredState,
    ) (LabArtifact, error)
}
```

输出：

```text
Containerlab Topology
FRR Configuration
Server Bootstrap Configuration
Daemon Manifest
Program Manifest
Validation Manifest
Allocation Manifest
```

Compiler 不负责分配 IP、ASN、VNI，也不负责决定拓扑关系。

### 11.3 Runtime Agent

宿主机或 Linux VM 内运行 `dcnetlab-agent`，负责高权限网络操作：

* 创建和删除 veth。
* 配置接口地址。
* 配置 MTU。
* 创建 Bridge、VRF 和 VXLAN。
* 进入 Network Namespace。
* 抓取报文。
* 配置 qdisc。
* 管理 nftables。
* 查询 Namespace 和接口状态。

接口使用 gRPC，不提供通用命令执行。

## 12. Planner、Reconciler 与 Operation

### 12.1 Plan

```go
type Plan struct {
    ID             string
    LabID          string
    BaseGeneration int64
    NewGeneration  int64
    Operations     []PlanOperation
    Allocations    []Allocation
    Warnings       []PlanWarning
}
```

Plan Operation：

```text
CreateNode
UpdateNode
DeleteNode
CreateLink
DeleteLink
AllocateAddress
ReleaseAddress
AllocateASN
ReleaseASN
CreateServer
InstallProgram
UpdateProgram
RemoveProgram
RenderConfiguration
DeployTopology
ValidateControlPlane
ValidateDataPlane
```

### 12.2 Apply 流程

```text
Validate Request
      ↓
Lock Lab
      ↓
Generate Plan
      ↓
Reserve Resources
      ↓
Persist New Generation
      ↓
Compile Artifacts
      ↓
Apply Runtime
      ↓
Start Required Programs
      ↓
Observe Runtime
      ↓
Validate Control Plane
      ↓
Validate Data Plane
      ↓
Commit Allocations
      ↓
Update Observed Generation
```

### 12.3 Operation

所有异步操作统一使用 Operation。

```go
type Operation struct {
    ID        string
    LabID     string
    Type      OperationType
    Resource  ResourceRef
    State     OperationState
    Progress  int
    Steps     []OperationStep
    Error     *OperationError
    CreatedAt time.Time
    UpdatedAt time.Time
}
```

状态：

```text
Queued
Running
Succeeded
Failed
Cancelled
```

HTTP 写请求返回：

```json
{
  "operationId": "op-01J..."
}
```

前端通过 WebSocket 或轮询获取进度。

## 13. Observer 设计

Observer 周期性采集实际状态：

| 状态              | 默认周期 |
| --------------- | ---: |
| Container State |  2 秒 |
| Interface State |  2 秒 |
| BGP Neighbor    |  3 秒 |
| Route Count     |  5 秒 |
| Program State   |  2 秒 |
| Daemon Health   |  5 秒 |
| Traffic Metrics |  1 秒 |
| Capture State   |  1 秒 |

统一观察结构：

```go
type NodeObservation struct {
    NodeID       string
    Timestamp    time.Time
    RuntimeState RuntimeState
    Interfaces   []InterfaceObservation
    BGPNeighbors []BGPNeighborObservation
    RouteCount   int
}

type ServerObservation struct {
    ServerID  string
    Programs  []ProgramObservation
    CPU       float64
    Memory    uint64
    Timestamp time.Time
}
```

Observer 只更新 Observed State，不主动修改 Desired State。

## 14. Server 与业务程序 UI

Server 详情页包含：

```text
Overview
Network
Programs
Daemons
Workloads
Storage
Traffic
Capture
Logs
Faults
```

### 14.1 Programs 页面

展示：

| 字段       | 说明                        |
| -------- | ------------------------- |
| Name     | 程序名称                      |
| Kind     | Application、Daemon、Probe  |
| Package  | Image 或 Binary            |
| Version  | 版本                        |
| State    | Running、Stopped、Unhealthy |
| PID      | Server 内 PID              |
| Restarts | 重启次数                      |
| CPU      | CPU 使用率                   |
| Memory   | 内存使用                      |
| Health   | 健康状态                      |

支持操作：

```text
Install
Start
Stop
Restart
Update
View Logs
View Configuration
Open Capture
Remove
```

### 14.2 Daemons 页面

额外展示：

```text
Auto Start
Required
Startup Order
Restart Policy
Readiness
Liveness
Last Heartbeat
```

用户可以从 Daemon Catalog 启用预定义 Daemon：

```text
Host Observer
Pingmesh Agent
HTTP Health Probe
DNS Probe
Custom Daemon
```

## 15. Traffic 子系统

### 15.1 内建程序

提供统一的 Golang `dcnetlab-lab-app`：

```text
http-server
http-client
tcp-server
tcp-client
udp-server
udp-client
dns-server
dns-client
file-server
file-client
grpc-server
grpc-client
```

该程序可作为：

* Server Program。
* Server Daemon。
* Isolated Workload。
* Traffic Generator。

### 15.2 Traffic Scenario

```go
type TrafficScenario struct {
    Meta ResourceMeta
    Spec TrafficScenarioSpec
    Status TrafficScenarioStatus
}

type TrafficScenarioSpec struct {
    Source      EndpointRef
    Destination Destination
    Protocol    TrafficProtocol
    Rate        float64
    Concurrency int
    PayloadSize int
    Duration    time.Duration
    Assertions  []TrafficAssertion
}
```

### 15.3 指标

```text
Request Rate
Success Rate
Error Rate
P50 Latency
P95 Latency
P99 Latency
Active Connections
Bytes Sent
Bytes Received
TCP Connection Errors
```

第一阶段指标保存在 Controller 内存中的固定窗口，并定期写入轻量文件。暂不引入 Prometheus Server，但提供 Prometheus 格式 Metrics Endpoint。

## 16. UI 抓包设计

### 16.1 采集架构

```text
Vue Capture UI
       ↓
Capture API
       ↓
Capture Manager
       ↓
Runtime Agent
       ↓
setns
       ↓
AF_PACKET
       ↓
Packet Decoder
       ├── Metadata Stream
       └── PCAPNG Writer
```

第一阶段不依赖用户安装 tcpdump、tshark 或 Wireshark。

### 16.2 Capture Session

```go
type CaptureSession struct {
    Meta ResourceMeta
    Spec CaptureSpec
    Status CaptureStatus
}

type CaptureSpec struct {
    NodeID       string
    InterfaceID  string
    Direction    CaptureDirection
    Filter       CaptureFilter
    Duration     time.Duration
    SnapLength   int
    MaxPackets   uint64
    MaxBytes     uint64
    SavePayload  bool
}
```

### 16.3 支持协议

第一阶段：

```text
Ethernet
VLAN
ARP
IPv4
IPv6
ICMP
TCP
UDP
VXLAN
```

第二阶段：

```text
BGP
BFD
DNS
DHCP
HTTP
```

### 16.4 UI

```text
Capture Target
Structured Filter
Packet List
Protocol Tree
Raw Hex
ASCII
Outer Packet
Inner VXLAN Packet
```

浏览器实时接收 Packet Metadata。用户打开某条报文时，再获取完整 Payload。

### 16.5 资源限制

| 参数          |       默认值 |
| ----------- | --------: |
| 默认时长        |      30 秒 |
| 最大时长        |     10 分钟 |
| Snap Length | 256 Bytes |
| 最大文件        |    100 MB |
| 单节点并发       |         2 |
| PCAP 保留     |     24 小时 |
| Metadata 保留 |       7 天 |

## 17. 故障注入设计

第一阶段支持：

```text
Link Down
Interface Down
Node Stop
Node Restart
Delay
Jitter
Packet Loss
Rate Limit
```

执行方式：

| 故障             | 实现                         |
| -------------- | -------------------------- |
| Node Stop      | Docker Engine API          |
| Interface Down | Netlink                    |
| Link Down      | 两端接口状态控制                   |
| Delay          | qdisc/netem                |
| Loss           | qdisc/netem                |
| Rate Limit     | qdisc                      |
| Program Stop   | Server Agent               |
| Daemon Crash   | Server Agent Fault Adapter |

故障恢复必须基于故障前快照，而不是恢复到写死默认值。

## 18. 虚拟网络扩展设计

### 18.1 VPC 映射

| 云资源            | Linux 对象             |
| -------------- | -------------------- |
| VPC            | VRF + Route Table    |
| Subnet         | Bridge + Gateway     |
| Workload Port  | veth                 |
| L2VNI          | VXLAN Device         |
| L3VNI          | VRF-associated VXLAN |
| Security Group | nftables             |
| EIP            | SNAT/DNAT            |
| Route Table    | VRF Routing Table    |

### 18.2 Overlay 阶段

阶段一：

```text
Controller Programming
```

Controller 直接写入：

```text
FDB
Neighbor
Route
VXLAN Device
VRF
```

阶段二：

```text
BGP EVPN
```

FRR EVPN 用作 MAC/IP 和前缀分发控制平面。FRR 文档将 BGP EVPN 定义为承载桥接或路由 Ethernet 服务的控制平面，并支持 VXLAN VNI 映射，适合作为后续 Overlay Backend。

### 18.3 EIP

```text
Workload Private IP
        ↓ VXLAN
EGW
        ↓ SNAT / DNAT
DC Edge Gateway
        ↓
External Router
```

DC Edge Gateway 负责物理出口路由，EGW 负责 VPC EIP 和 NAT，两者职责保持分离。

## 19. 数据存储

### 19.1 SQLite

保存：

```text
Labs
Nodes
Links
Servers
Programs
Daemons
Workloads
Address Allocations
ASN Allocations
Traffic Scenarios
Capture Sessions
Fault Scenarios
Operations
Generation Snapshots
Audit Logs
```

### 19.2 文件系统

```text
data/
├── dcnetlab.db
├── labs/
│   └── <lab-id>/
│       ├── generations/
│       │   └── <generation>/
│       │       ├── desired-state.json
│       │       ├── plan.json
│       │       ├── topology.clab.yml
│       │       ├── allocation.json
│       │       └── configs/
│       ├── captures/
│       ├── program-logs/
│       ├── reports/
│       └── diagnostics/
├── artifacts/
│   ├── binaries/
│   └── images/
└── volumes/
```

### 19.3 Generation

每次 Apply 保存：

```text
Desired State Snapshot
Plan
Allocation Manifest
Runtime Artifact
Configuration Artifact
Validation Result
```

默认保留最近 10 个 Generation。

## 20. API 设计

### 20.1 Labs 与拓扑

```text
POST   /api/v1/labs
GET    /api/v1/labs
GET    /api/v1/labs/{id}
DELETE /api/v1/labs/{id}

POST   /api/v1/labs/{id}/plans
POST   /api/v1/plans/{id}/apply

GET    /api/v1/labs/{id}/nodes
GET    /api/v1/labs/{id}/links
```

### 20.2 Server

```text
GET    /api/v1/servers
GET    /api/v1/servers/{id}
POST   /api/v1/servers/{id}/start
POST   /api/v1/servers/{id}/stop
POST   /api/v1/servers/{id}/restart
```

### 20.3 Program 和 Daemon

```text
POST   /api/v1/programs
GET    /api/v1/programs/{id}
PUT    /api/v1/programs/{id}
DELETE /api/v1/programs/{id}

POST   /api/v1/programs/{id}/start
POST   /api/v1/programs/{id}/stop
POST   /api/v1/programs/{id}/restart

GET    /api/v1/programs/{id}/logs
GET    /api/v1/programs/{id}/health

POST   /api/v1/daemons
GET    /api/v1/daemons/{id}
POST   /api/v1/daemons/{id}/enable
POST   /api/v1/daemons/{id}/disable
```

### 20.4 Traffic、Capture 和 Fault

```text
POST   /api/v1/traffic-scenarios
POST   /api/v1/traffic-scenarios/{id}/start
POST   /api/v1/traffic-scenarios/{id}/stop

POST   /api/v1/capture-sessions
GET    /api/v1/capture-sessions/{id}
POST   /api/v1/capture-sessions/{id}/stop

POST   /api/v1/fault-scenarios
POST   /api/v1/fault-scenarios/{id}/apply
POST   /api/v1/fault-scenarios/{id}/recover
```

### 20.5 WebSocket

```text
/ws/v1/labs/{id}/topology
/ws/v1/operations/{id}
/ws/v1/servers/{id}/programs
/ws/v1/traffic-scenarios/{id}
/ws/v1/capture-sessions/{id}
/ws/v1/fault-scenarios/{id}
```

### 20.6 错误格式

```json
{
  "code": "PROGRAM_START_FAILED",
  "message": "Failed to start program on server",
  "requestId": "req-01J...",
  "resource": {
    "type": "program",
    "id": "program-01J..."
  },
  "details": {
    "serverId": "server-01J...",
    "reason": "executable not found"
  }
}
```

## 21. 安全设计

* Controller 默认只监听 localhost。
* Agent 仅接受 Controller 连接。
* Agent 使用 Unix Socket 或双向 TLS。
* 不暴露任意 Shell 执行接口。
* Program Binary 必须校验 SHA-256。
* OCI Image 支持 Allowlist。
* Program 运行使用最小 Capability。
* 默认禁止挂载 Docker Socket。
* Volume 路径由平台生成。
* 抓包限制时长、大小和并发。
* BPF Filter 经过校验。
* 所有写操作记录 Audit Log。
* Program Config 中的 Secret 使用独立 Secret Store。
* 前端不显示宿主机真实敏感路径。

## 22. 可观测性

### 22.1 日志

统一字段：

```text
timestamp
level
component
request_id
operation_id
lab_id
resource_type
resource_id
message
error
```

### 22.2 指标

```text
dcnetlab_operation_total
dcnetlab_operation_failed_total
dcnetlab_reconcile_duration_seconds
dcnetlab_runtime_node_total
dcnetlab_bgp_neighbor_up
dcnetlab_program_running
dcnetlab_program_restart_total
dcnetlab_daemon_health
dcnetlab_capture_packet_total
dcnetlab_capture_drop_total
dcnetlab_traffic_request_total
dcnetlab_traffic_request_failed_total
```

### 22.3 诊断包

用户可通过 UI 导出：

```text
Desired State
Runtime State
Recent Operations
Generated Configuration
FRR Status
Program Status
Daemon Status
Controller Logs
Agent Logs
```

默认不导出业务 Payload 和完整 PCAP，除非用户显式选择。

## 23. 仓库结构

```text
dcnetlab/
├── cmd/
│   ├── dcnetlab/
│   ├── controller/
│   ├── runtime-agent/
│   ├── server-agent/
│   └── lab-app/
├── internal/
│   ├── api/
│   ├── model/
│   ├── store/
│   ├── operation/
│   ├── topology/
│   ├── planner/
│   ├── allocator/
│   │   ├── ipam/
│   │   ├── asn/
│   │   └── vni/
│   ├── reconciler/
│   ├── compiler/
│   │   ├── containerlab/
│   │   ├── frr/
│   │   ├── server/
│   │   └── daemon/
│   ├── runtime/
│   │   ├── containerlab/
│   │   ├── docker/
│   │   └── linux/
│   ├── observer/
│   ├── server/
│   ├── program/
│   ├── daemon/
│   │   ├── generic/
│   │   └── providers/
│   ├── workload/
│   ├── traffic/
│   ├── capture/
│   ├── fault/
│   ├── virtualnetwork/
│   └── audit/
├── proto/
│   ├── runtime_agent.proto
│   └── server_agent.proto
├── web/
│   ├── src/
│   │   ├── api/
│   │   ├── components/
│   │   ├── composables/
│   │   ├── layouts/
│   │   ├── pages/
│   │   ├── router/
│   │   ├── stores/
│   │   ├── types/
│   │   ├── utils/
│   │   ├── App.vue
│   │   └── main.ts
│   ├── vite.config.ts
│   └── package.json
├── images/
│   ├── frr-node/
│   ├── compute-server/
│   └── lab-app/
├── profiles/
│   ├── micro.yaml
│   └── standard.yaml
├── templates/
│   ├── frr/
│   ├── server/
│   └── daemon/
├── deploy/
│   ├── compose.yaml
│   └── lima/
├── test/
│   ├── integration/
│   ├── e2e/
│   └── fixtures/
└── Makefile
```

## 24. Linux 与 macOS 部署

### 24.1 Linux

依赖：

```text
Docker Engine
DCNetLab Launcher
Web Browser
```

启动：

```text
dcnetlab up
```

### 24.2 macOS

数据面必须运行在 Linux VM 中。

```text
DCNetLab Launcher
       ↓
Create or Start Lima VM
       ↓
Start Docker and Runtime Agent
       ↓
Start Controller and Vue UI
       ↓
Forward localhost:8080
```

第一版固定使用一种 VM Backend，避免同时兼容 Lima、Colima、OrbStack 和 Docker Desktop 的不同网络行为。

## 25. 测试设计

### 25.1 单元测试

覆盖：

* IPAM 分配和回收。
* ASN 分配。
* Planner。
* 资源状态机。
* Program 生命周期。
* Daemon Restart Policy。
* 配置渲染。
* Packet Decoder。
* API 参数校验。

### 25.2 Golden Test

固定 Desired State，验证输出：

```text
Containerlab YAML
FRR Configuration
Server Bootstrap Configuration
Daemon Configuration
```

### 25.3 集成测试

最小拓扑：

```text
Server-A
   |
 Leaf-A
   |
 Spine
   |
 Leaf-B
   |
Server-B
```

验证：

* BGP Established。
* 路由存在。
* Server 可通信。
* Program 可启动。
* Daemon 自动重启。
* HTTP Traffic 成功。
* Capture 可读取报文。
* Link Down 后业务中断。
* Link 恢复后业务恢复。

### 25.4 前端测试

Vitest：

* Store。
* Composable。
* API Error Mapping。

Vue Test Utils：

* Program Table。
* Capture List。
* Node Drawer。
* Operation Panel。

Playwright：

```text
Create Lab
Apply Plan
Open Topology
Deploy Program
Enable Daemon
Start Traffic
Start Capture
Inject Fault
Inspect Result
Delete Lab
```

## 26. 实施计划

### Iteration 0：运行时验证

交付：

* Golang Controller 骨架。
* Vue 3 页面骨架。
* Containerlab 创建两节点拓扑。
* FRR BGP 建立。
* Docker Go SDK 调用。
* WebSocket 状态推送。

退出标准：

```text
页面能创建实验并看到两个 BGP 节点状态
```

### Iteration 1：物理网络闭环

交付：

* Micro Profile。
* DC Edge、SuperSpine、Spine、Leaf、Server。
* IPAM。
* ASN Allocator。
* FRR 配置编译。
* Topology UI。
* Plan 和 Apply。

### Iteration 2：Server Program

交付：

* Compute Server Container。
* Server Agent。
* Program 模型。
* Program 安装、启动、停止和日志。
* `lab-app` HTTP/TCP/UDP 模式。
* Program UI。

退出标准：

```text
用户可以从 UI 在 Server 上启动真实 HTTP 程序
```

### Iteration 3：Daemon Framework

交付：

* Daemon 模型。
* Auto Start。
* Restart Policy。
* 健康检查。
* 配置渲染。
* Generic Daemon Provider。
* Daemon UI。

退出标准：

```text
Server 重启后 Required Daemon 自动恢复
```

### Iteration 4：Traffic

交付：

* Traffic Scenario。
* HTTP、TCP、UDP Client。
* RPS、成功率和时延。
* Traffic UI。

### Iteration 5：原生抓包

交付：

* AF_PACKET。
* Packet Metadata。
* Packet Detail。
* PCAPNG。
* Capture UI。
* 抓包资源限制。

### Iteration 6：故障实验

交付：

* Link Down。
* Node Stop。
* Delay。
* Loss。
* Rate Limit。
* 故障恢复。
* Traffic 和 Capture 联动。

### Iteration 7：扩容和 Generation

交付：

* Add Pod。
* Add Rack。
* Add Leaf。
* Add Server。
* Plan Diff。
* Generation Snapshot。
* Rollback。

### Iteration 8：Pingmesh 集成

交付：

* Pingmesh Daemon Provider。
* Target Planner。
* Daemon 配置下发。
* 探测结果采集。
* Pingmesh UI。
* 拓扑质量视图。

### Iteration 9：虚拟网络

交付：

* Workload 独立 Namespace。
* VPC。
* Subnet。
* VRF。
* VXLAN。
* L2VNI 和 L3VNI。

### Iteration 10：EIP

交付：

* EGW。
* Public IP Pool。
* EIP Binding。
* SNAT。
* DNAT。
* 模拟公网服务。
* NAT 前后抓包。

## 27. 关键架构决策

### ADR-001：后端统一使用 Golang

原因：

* 适合 Controller、Agent 和网络工具开发。
* 支持静态编译和单二进制交付。
* 标准库具备成熟的并发、网络和 HTTP 能力。
* Docker、gRPC、Netlink 和 eBPF 生态较完整。
* Controller、Runtime Agent、Server Agent 和测试程序可以统一语言。

### ADR-002：前端使用 Vue 3

原因：

* Composition API 适合按领域组织复杂交互逻辑。
* TypeScript 支持较完整。
* Pinia 可以按领域拆分状态。
* 与 Cytoscape.js 和 ECharts 集成直接。
* 适合快速迭代管理后台和网络操作页面。

### ADR-003：Program 与 Daemon 共用执行模型

原因：

* 两者的安装、配置、启动、停止、日志和健康检查高度一致。
* Daemon 仅增加 Auto Start、Restart、Readiness 和依赖关系。
* 避免建设两套重复的进程管理系统。
* Pingmesh 等后续能力可以通过 Provider 扩展。

### ADR-004：Server Daemon 由内部 Agent 管理

原因：

* Server Container 中不依赖 systemd。
* 生命周期可通过统一 API 控制。
* 状态可以直接上报 Controller。
* 更容易支持日志、健康检查和故障注入。
* 避免依赖外部 Supervisor。

### ADR-005：第一版使用 Containerlab

原因：

* 能快速创建节点和虚拟链路。
* 支持容器化网络设备。
* 降低拓扑运行时实现成本。
* 后续通过 Runtime Driver 替换，不侵入领域模型。

### ADR-006：第一版使用 SQLite

原因：

* 无外部服务依赖。
* 支持事务。
* 便于本地备份和导出。
* 可以通过 Repository 抽象迁移到 PostgreSQL。

### ADR-007：抓包使用 AF_PACKET

原因：

* 无需用户安装外部抓包工具。
* 可以与 Namespace、权限和 UI 集成。
* 支持内核过滤和高效报文读取。
* 后续可以增加 eBPF Capture Backend。

## 28. 第一版完成标准

第一版必须满足：

1. Linux 上一条命令启动。
2. macOS 上自动创建或启动 Linux VM。
3. Vue 3 UI 可以创建 Micro Fabric。
4. 网络设备运行真实 FRR BGP。
5. Server 可以通过 Fabric 通信。
6. UI 可以查看节点、接口、BGP 和路由状态。
7. Server Agent 可以运行真实程序。
8. Program 可以通过 UI 启动、停止和查看日志。
9. Required Daemon 可以自动启动和异常重启。
10. HTTP Client 可以持续访问 HTTP Server。
11. UI 可以显示请求率、成功率和时延。
12. UI 可以在节点接口上启动抓包。
13. 用户不需要安装 tcpdump、tshark 或 Wireshark。
14. UI 可以注入 Link Down、Delay 和 Loss。
15. UI 可以观察网络故障对程序流量的影响。
16. Apply 操作具备 Plan、进度和结构化错误。
17. 系统重启后 Desired State、Program 和 Daemon 定义可以恢复。
18. 代码结构为后续 Pingmesh、VPC、VXLAN 和 EIP 提供明确扩展点。

## 29. 实现优先级

严格按照以下顺序推进：

```text
Golang Controller 和 Runtime
        ↓
物理 Clos 网络
        ↓
Vue 3 拓扑和状态页面
        ↓
Server Agent
        ↓
Program 执行框架
        ↓
Daemon 生命周期框架
        ↓
真实业务流量
        ↓
原生 UI 抓包
        ↓
故障实验
        ↓
可视化扩容
        ↓
Pingmesh
        ↓
VPC / VXLAN
        ↓
EIP / NAT
        ↓
BGP EVPN
```

在 Server Program、Daemon、Traffic 和 Capture 闭环稳定之前，不应提前投入复杂 EVPN、多 IDC、分布式控制器或完整云网络能力。

DCNetLab 第一阶段的核心价值是：用户可以在一个统一的 Vue 3 页面中创建真实数据中心网络，在 Server 上运行真实程序和常驻 Daemon，产生持续流量，注入故障，并直接观察路由状态、业务指标和真实报文。
