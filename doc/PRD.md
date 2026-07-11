# DCNetLab 数据中心网络模拟平台需求文档

## 1. 文档信息

| 项目   | 内容                              |
| ---- | ------------------------------- |
| 项目名称 | DCNetLab                        |
| 文档类型 | 产品需求文档 / 软件需求规格说明书              |
| 文档版本 | V1.0                            |
| 项目阶段 | 立项与总体设计                         |
| 目标平台 | Linux、macOS                     |
| 主要用户 | 网络工程师、网络软件工程师、SRE、云网络研发、网络学习者   |
| 核心定位 | 面向数据中心物理网络和虚拟网络的本地化、可视化、可交互模拟平台 |

## 2. 项目背景

数据中心网络涉及物理拓扑、路由控制平面、主机网络、Overlay、VPC、EIP、NAT、故障收敛、业务流量和报文分析等多个层次。现有实验方式通常依赖大量命令行工具、手工配置文件和分散的模拟组件。使用者需要自行准备 Containerlab、FRR、Linux Network Namespace、tcpdump、Wireshark、流量发生器和业务容器，并理解各工具的使用方式，才能搭建一个相对完整的数据中心网络实验环境。

这种方式存在以下问题：

1. 环境搭建复杂，Linux 和 macOS 上的运行方式不一致。
2. 物理网络、虚拟网络、业务流量和抓包分析相互割裂。
3. 拓扑扩容需要手工修改配置，容易产生地址、ASN 和链路冲突。
4. 抓包依赖命令行和外部工具，不适合快速实验和教学展示。
5. Server 通常只作为网络节点存在，无法模拟真实服务、真实数据和持续业务流量。
6. 缺少统一的 Desired State、变更计划、状态收敛和故障实验模型。
7. 大量实验结果只能依赖人工判断，缺乏自动验收和可重复性。

DCNetLab 旨在提供一个轻量但完整的数据中心网络模拟平台。系统在单台 Linux 或 macOS 设备上即可运行，通过 Web UI 完成拓扑创建、物理网络部署、Server 管理、业务部署、流量生成、抓包分析、故障注入和虚拟网络配置。

## 3. 项目目标

### 3.1 总体目标

构建一套可在个人电脑或开发机上快速运行的数据中心网络模拟系统，能够以较小规模复现大型数据中心的关键网络层次和运行机制，并为后续 VPC、VXLAN、EIP、Security Group、NAT Gateway 和 EVPN 等虚拟网络能力提供统一扩展基础。

系统应满足以下核心目标：

1. 一条命令或图形化启动器完成运行环境启动。
2. 通过 Web UI 创建和管理完整的数据中心物理网络。
3. 支持典型 Clos 架构，包括 DCGW、SuperSpine、Spine、Leaf、Server。
4. 使用真实 Linux 网络栈和真实路由协议实现数据转发。
5. Server 可以运行真实容器化应用并保存真实数据。
6. 通过平台内建的流量系统持续生成真实业务流量。
7. 通过 Web UI 在任意节点和接口上进行抓包和报文分析。
8. 支持可视化扩容 Pod、Rack、Leaf 和 Server。
9. 支持链路故障、设备故障、丢包、延迟和限速等实验。
10. 在物理网络上扩展 VPC、Subnet、VNI、VXLAN、VRF、EIP 和 NAT。
11. 所有拓扑、配置和实验均可保存、复现和回滚。
12. 尽量减少用户对命令行和外部工具的依赖。

### 3.2 非目标

第一阶段不以精确模拟交换芯片硬件行为为目标，不覆盖以下能力：

1. 交换 ASIC Buffer 和芯片 Pipeline 的精确模拟。
2. 真实 SerDes、光模块和物理层信号模拟。
3. 线速 PPS 和 Tbps 级吞吐模拟。
4. PFC、RoCE、ECN 等无损网络的硬件级行为。
5. 厂商私有 NOS 的完整仿真。
6. 大规模生产环境性能压测。
7. 完整替代 EVE-NG、GNS3、NS-3 或商业网络仿真器。
8. 第一版不依赖 Kubernetes，也不以 Kubernetes 集群为运行前提。

## 4. 用户角色

### 4.1 网络架构师

使用平台验证 Clos 拓扑、BGP 策略、ECMP、故障收敛、跨 Pod 通信和虚拟网络架构。

### 4.2 网络软件工程师

使用平台开发 IPAM、拓扑编排、网络控制器、Host Agent、EVPN、NAT 和网络可观测能力。

### 4.3 SRE 和运维工程师

使用平台复现网络故障、观察业务影响、验证 SOP、抓包和分析收敛过程。

### 4.4 云网络研发

使用平台验证 VPC、Subnet、VXLAN、VRF、EIP、NAT Gateway、Security Group 和 Overlay 控制平面。

### 4.5 学习和培训用户

通过可视化界面观察报文在 Leaf-Spine Fabric、VXLAN 和 EIP Gateway 中的转发过程。

## 5. 总体范围

系统由以下七个核心子系统组成：

| 子系统                 | 主要职责                               |
| ------------------- | ---------------------------------- |
| Topology            | 数据中心资源建模、拓扑创建、扩容和可视化               |
| Runtime             | Linux 网络、容器、FRR、链路和设备生命周期          |
| Server and Workload | 模拟物理 Server、VM、应用和持久化数据            |
| Traffic             | 真实流量生成、业务指标和测试断言                   |
| Capture             | UI 抓包、协议解析、PCAP 保存和路径关联            |
| Fault               | 链路、设备、时延、丢包、限速和故障场景                |
| Virtual Network     | VPC、Subnet、VNI、VXLAN、VRF、EIP 和 NAT |

## 6. 系统总体架构要求

系统采用声明式控制架构。用户在 UI 中提交期望状态，Controller 根据当前状态和期望状态生成变更计划，并驱动底层 Runtime 收敛。

```text
Web UI
  │
  ├── Topology
  ├── Server
  ├── Workload
  ├── Traffic
  ├── Capture
  ├── Fault
  └── Virtual Network
  │
DCNetLab Controller
  ├── API Server
  ├── Inventory Service
  ├── Topology Planner
  ├── IPAM / ASN / VNI Allocator
  ├── Reconciler
  ├── Runtime Driver
  ├── Capture Service
  ├── Traffic Service
  └── State Observer
  │
Linux Runtime
  ├── Container Runtime
  ├── FRR
  ├── Linux Network Namespace
  ├── veth / Bridge / VRF / VXLAN
  ├── nftables
  ├── AF_PACKET Capture
  └── Workload Containers
```

系统必须遵循以下原则：

1. UI 不直接执行 Shell 命令。
2. Containerlab YAML 不是系统的唯一数据源。
3. 系统自己的资源模型是 Desired State 的唯一事实来源。
4. Containerlab、FRR、Linux 网络和 nftables 配置均为编译产物。
5. 所有变更必须具备 Plan、Apply、Observe 和 Rollback 生命周期。
6. 正常使用流程不得依赖用户手工执行网络命令。

## 7. 物理网络需求

### 7.1 拓扑层次

系统至少支持以下层次：

```text
Region
  └── IDC
       ├── External Network
       ├── DC Gateway
       ├── SuperSpine
       └── Pod
            ├── Spine
            ├── Rack
            ├── Leaf
            └── Server
```

第一版至少提供以下预定义 Profile：

| Profile  | 默认规模                          | 使用场景        |
| -------- | ----------------------------- | ----------- |
| Micro    | 1 Pod、2 Spine、2 Leaf、4 Server | 笔记本快速启动     |
| Standard | 2 Pod、4 Spine、4 Leaf、8 Server | 跨 Pod 和扩容实验 |
| Custom   | 用户自定义                         | 架构实验        |

### 7.2 Underlay

物理 Fabric 第一版采用三层 Clos 和 eBGP。

必须支持：

1. Leaf-Spine 三层互联。
2. Spine-SuperSpine 三层互联。
3. SuperSpine-DCGW 三层互联。
4. 点到点链路使用 `/31` 地址。
5. 网络设备具有独立 Loopback `/32`。
6. 每台网络设备分配独立私有 ASN。
7. FRR 运行 eBGP。
8. Linux Kernel FIB 负责真实转发。
9. 支持 ECMP。
10. 支持默认路由和公网前缀注入。
11. 支持查看 BGP Neighbor、RIB 和 Kernel Route。
12. 支持可选 BFD。

### 7.3 地址与 ASN 分配

系统必须内建 IPAM 和 ASN Allocator。

至少管理：

```text
Management IP Pool
Loopback Pool
Fabric Point-to-Point Pool
Server Uplink Pool
VTEP Pool
Public IP Pool
ASN Pool
L2VNI Pool
L3VNI Pool
VRF Table ID Pool
```

分配器必须满足：

1. 地址和 ASN 不重复。
2. 删除资源后支持回收。
3. 分配操作具备事务性。
4. Apply 失败时能够释放未提交资源。
5. 支持预览本次扩容将使用的资源。
6. 支持显示地址池使用率。

## 8. 拓扑可视化需求

### 8.1 拓扑页面

系统必须提供分层拓扑页面，并按照数据中心角色固定布局。

展示层级：

```text
External
DC Gateway
SuperSpine
Pod
Spine
Leaf
Server
Workload
```

每个节点至少展示：

```text
Name
Role
Runtime State
Management IP
Loopback
ASN
Interface State
BGP State
Route Count
Active Alarm
Workload Count
```

链路至少展示：

```text
Interface Name
IP Address
Administrative State
Operational State
Traffic Rate
Packet Loss
Delay
Active Capture
Fault State
```

### 8.2 节点操作

用户点击节点后，应能够在 UI 中执行：

1. 查看 Overview。
2. 查看接口。
3. 查看 BGP Neighbor。
4. 查看 BGP Route。
5. 查看 Kernel Route。
6. 查看 ARP 和 Neighbor。
7. 查看 Bridge 和 FDB。
8. 查看 VRF 和 VNI。
9. 发起 Ping。
10. 发起 Traceroute。
11. 启动抓包。
12. 查看日志。
13. 查看生成配置。
14. 停止、启动和重启设备。
15. 注入设备故障。

CLI Console 可以作为高级功能保留，但不得作为基础操作的必要路径。

## 9. 可视化扩容需求

系统必须支持从 UI 扩容以下资源：

```text
IDC
Pod
Spine
Rack
Leaf
Server
External Network
```

以 Add Pod 为例，用户只需要提供：

```text
Pod Name
Spine Count
Leaf Count
Rack Count
Servers per Rack
Uplink Mode
```

系统自动完成：

1. ASN 分配。
2. Loopback 分配。
3. 点到点地址分配。
4. 上联和下联生成。
5. FRR 配置生成。
6. Container 和 Linux 网络创建。
7. BGP 会话验证。
8. 路由学习验证。
9. Server 连通性验证。
10. 拓扑页面刷新。

扩容前必须提供 Plan 页面，展示：

```text
新增资源
修改资源
删除资源
地址分配
ASN 分配
配置差异
预计影响
```

第一版允许通过全量重建实现扩容，但必须保留 Desired State 和资源快照。后续版本支持增量创建节点、链路和配置。

## 10. Server 和 Workload 需求

### 10.1 Server 模型

Server 模拟一台接入 Leaf 的真实 Linux 计算节点，应具备：

1. 独立网络命名空间或容器运行环境。
2. 独立 Uplink。
3. Linux Bridge。
4. VRF。
5. VXLAN Interface。
6. VTEP IP。
7. Host Agent。
8. Workload 管理能力。
9. 数据持久化能力。
10. 资源使用状态。

Server 不是单纯的 Ping 节点，必须能够运行真实应用。

### 10.2 Workload 模型

Workload 表示运行在 Server 上的 VM 或应用实例。

第一版使用 OCI Container 模拟 Workload，要求：

1. 每个 Workload 具有独立 Network Namespace。
2. Workload 默认使用 `NetworkMode=none`。
3. Host Agent 创建 veth 并接入目标 Bridge。
4. Workload 可以加入指定 VPC 和 Subnet。
5. Workload 可以配置静态 Private IP。
6. Workload 可以挂载 Persistent Volume。
7. Workload 可以查看日志。
8. Workload 可以停止、启动和重启。
9. Workload 可以迁移至其他 Server。
10. Workload 可以绑定 EIP。

### 10.3 应用目录

第一版至少内建：

| 应用              | 用途            |
| --------------- | ------------- |
| HTTP Server     | HTTP 和 EIP 实验 |
| HTTP Client     | 持续请求          |
| TCP Echo Server | TCP 建连和重传     |
| UDP Echo Server | UDP 和丢包       |
| DNS Server      | DNS 流量        |
| File Server     | 大文件传输         |
| gRPC Server     | RPC 流量        |
| Generic Linux   | 自定义镜像         |
| Redis           | 有状态服务，可选      |
| PostgreSQL      | 有状态服务，可选      |

平台应优先提供统一的轻量 `lab-app` 镜像，避免默认加载大量重量级镜像。

### 10.4 数据持久化

Workload 支持三类存储策略：

| 类型         | 行为                  |
| ---------- | ------------------- |
| Ephemeral  | Workload 删除后数据删除    |
| Persistent | Workload 删除或重建后数据保留 |
| Snapshot   | 支持实验前快照和恢复          |

UI 应能够查看：

```text
Volume Name
Owner
Size
Used Capacity
Persistence Mode
Snapshot Count
Last Snapshot
```

## 11. 流量系统需求

### 11.1 Traffic Scenario

系统必须允许用户通过 UI 创建真实流量场景。

至少支持：

```text
ICMP
TCP Stream
UDP Stream
HTTP
gRPC
DNS
File Transfer
PostgreSQL Query
Redis Request
```

场景参数至少包括：

```text
Source
Destination
Protocol
Request Rate
Connection Count
Payload Size
Duration
Schedule
Timeout
Retry Policy
```

### 11.2 内建流量发生器

系统提供平台内建 `lab-loadgen`，不得要求用户安装 curl、iperf3、wrk 或其他流量工具。

`lab-loadgen` 至少支持：

1. ICMP 探测。
2. TCP 长连接和短连接。
3. UDP PPS 和带宽控制。
4. HTTP Method、Path、Header、Body。
5. gRPC Method 调用。
6. DNS Query。
7. PostgreSQL Query。
8. Redis Command。
9. 并发控制。
10. 固定速率和阶梯压测。
11. 持续运行。
12. 指标上报。

### 11.3 流量指标

UI 应展示：

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
TCP Retransmissions
Response Code Distribution
```

### 11.4 测试断言

Traffic Scenario 支持定义断言：

```text
Success Rate >= 99.9%
P99 Latency < 100 ms
Packet Loss < 0.1%
Maximum Interruption < 1 s
```

实验完成后自动输出 Pass 或 Fail。

## 12. UI 抓包需求

### 12.1 总体要求

抓包必须是系统原生 UI 能力。用户不需要安装或操作：

```text
tcpdump
tshark
Wireshark
libpcap CLI
```

用户应能够从以下入口启动抓包：

1. 节点详情页。
2. 接口详情页。
3. 拓扑链路。
4. Workload 页面。
5. VPC 页面。
6. EIP 页面。
7. 故障实验页面。
8. Flow Path 页面。

### 12.2 抓包参数

UI 至少支持：

```text
Target Node
Interface
Direction
Protocol
Source
Destination
Source Port
Destination Port
VLAN
VNI
Duration
Maximum Packets
Maximum Bytes
Snap Length
Save Payload
```

默认采用结构化过滤器，高级模式可提供表达式输入，但必须经过语法校验和安全编译。

### 12.3 Capture Agent

Capture Agent 使用 Linux 原生能力采集报文：

```text
setns
AF_PACKET
TPACKET_V3
Socket BPF
mmap Ring Buffer
```

正常抓包流程不得启动 tcpdump 子进程。

Capture Agent 至少提供：

1. 报文实时读取。
2. 内核态过滤。
3. 协议解析。
4. 报文元数据推送。
5. PCAPNG 保存。
6. 抓包资源限制。
7. 会话停止和超时。
8. Capture Session 查询。

### 12.4 协议解析

第一版至少解析：

| 层次          | 协议                                      |
| ----------- | --------------------------------------- |
| L2          | Ethernet、VLAN、ARP、LLDP                  |
| L3          | IPv4、IPv6、ICMP、ICMPv6                   |
| L4          | TCP、UDP                                 |
| Routing     | BGP、BFD                                 |
| Overlay     | VXLAN                                   |
| Application | DNS、DHCP、HTTP/1.1                       |
| Diagnostic  | TCP SYN、FIN、RST、Retransmission、Fragment |

VXLAN 报文必须同时展示 Outer Header 和 Inner Packet。

### 12.5 报文页面

Packet List 至少展示：

```text
Timestamp
Ingress/Egress
Source
Destination
Protocol
Length
Summary
Capture Point
```

点击报文后展示：

```text
Protocol Tree
Decoded Fields
Raw Hex
ASCII
Outer Packet
Inner Packet
Related Packet
```

### 12.6 双端链路抓包

用户点击物理链路后，应支持同时抓取链路两端。

系统应尝试基于以下字段进行关联：

```text
Five Tuple
IP ID
TCP Sequence
Packet Length
Payload Hash
Timestamp
```

关联结果应展示：

```text
Matched
Unmatched
Estimated Delay
TTL Change
NAT Change
Encapsulation Change
Potential Drop
```

### 12.7 Capture Along Path

系统应支持用户选择 Source 和 Destination 后，在沿途关键节点自动抓包。

流程如下：

1. 根据路由、ECMP、VNI 和 NAT 状态计算预期路径。
2. 在路径关键接口上创建 Capture Session。
3. 触发一次测试流量。
4. 对报文进行逐跳关联。
5. 在 UI 展示报文形态变化。

典型展示：

```text
VM Private Packet
Host VXLAN Packet
EGW Decapsulated Packet
EGW SNAT Packet
External Packet
```

### 12.8 抓包资源限制

默认限制：

| 参数             |       默认值 |
| -------------- | --------: |
| 默认时长           |      30 秒 |
| 最大时长           |     10 分钟 |
| 默认 Snap Length | 256 Bytes |
| 最大文件           |    100 MB |
| 单节点并发会话        |         2 |
| PCAP 保留        |     24 小时 |
| 元数据保留          |       7 天 |

## 13. 故障注入需求

系统必须支持从 UI 注入以下故障：

```text
Interface Down
Link Down
Device Stop
Device Restart
Packet Loss
Delay
Jitter
Rate Limit
Packet Corruption
MTU Change
BGP Session Reset
Route Withdrawal
```

故障场景应支持：

1. 立即执行。
2. 定时执行。
3. 持续指定时间。
4. 自动恢复。
5. 与 Traffic Scenario 联动。
6. 与 Capture Session 联动。
7. 记录实验前后指标。
8. 生成实验报告。

故障实验报告至少包括：

```text
Failure Start Time
Failure End Time
Affected Nodes
BGP Convergence Time
Traffic Interruption
Failed Requests
Path Change
Before/After Route
Before/After Capture
Assertion Result
```

## 14. 虚拟网络需求

### 14.1 VPC

VPC 映射为 Linux VRF 和独立路由表。

VPC 资源至少包含：

```text
VPC ID
Name
CIDR
L3VNI
VRF Table ID
Route Table
Status
```

### 14.2 Subnet

Subnet 至少包含：

```text
Subnet ID
VPC ID
CIDR
Gateway
L2VNI
Availability Scope
Attached Workloads
```

Subnet 在 Host 上映射为：

```text
Linux Bridge
Gateway Interface
L2 VXLAN Interface
```

### 14.3 VXLAN

系统必须支持：

1. Host VTEP。
2. L2VNI。
3. L3VNI。
4. VXLAN 封装和解封装。
5. VTEP 可达性验证。
6. FDB 和 Neighbor 查看。
7. 跨 Server Workload 通信。
8. VXLAN 抓包解析。

### 14.4 Overlay 控制平面

系统分阶段支持：

第一阶段：

```text
Controller Programming
```

Controller 直接维护 Endpoint、MAC、Private IP、VTEP 和 VNI，并写入 Linux FDB、Neighbor 和 Route。

第二阶段：

```text
BGP EVPN
```

使用 FRR EVPN 分发 MAC/IP 和 Prefix。

系统模型必须避免将 Overlay 控制平面写死为单一实现。

## 15. EIP 和公网需求

### 15.1 EIP

EIP 资源至少包含：

```text
EIP Address
Binding Workload
Private IP
VPC
Gateway
Status
NAT Session Count
```

用户可以在 UI 中：

1. 申请 EIP。
2. 绑定 Workload。
3. 解绑 EIP。
4. 查看 NAT 规则。
5. 查看 Conntrack。
6. 启动抓包。
7. 查看公网流量。

### 15.2 EIP Gateway

第一版采用集中式 EGW。

EGW 应支持：

```text
VXLAN Termination
VRF
SNAT
DNAT
Conntrack
Public Prefix Advertisement
Default Route
```

出方向流程：

```text
Private Packet
VXLAN Encapsulation
EGW Decapsulation
SNAT
External Forwarding
```

入方向流程：

```text
External Packet
DNAT
VXLAN Encapsulation
Host Decapsulation
Workload Delivery
```

### 15.3 模拟公网

系统提供 External Network 和 External Server。

External Server 可以运行真实 HTTP、DNS 或 TCP 服务，用于验证：

```text
Default Route
EIP
SNAT
DNAT
Return Path
MTU
Failure
```

## 16. 配置和状态管理需求

### 16.1 Desired State

系统必须保存：

```text
Lab
Topology
Node
Link
Address Pool
ASN Pool
Server
Workload
Traffic Scenario
Capture Session
Fault Scenario
VPC
Subnet
EIP
```

### 16.2 Generation

所有可变资源至少包含：

```text
generation
observedGeneration
desiredState
runtimeState
lastError
```

### 16.3 Plan 和 Apply

Apply 前生成：

```text
Desired State Snapshot
Change Plan
Resource Allocation
Topology Artifact
FRR Configuration
Linux Configuration
Validation Result
```

### 16.4 Rollback

系统至少保留最近 10 个 Generation。

Rollback 应能够恢复：

```text
Topology
Configuration
Resource Binding
Workload Definition
Virtual Network Definition
```

业务持久化数据默认不随网络 Rollback 回退，除非用户显式选择恢复 Volume Snapshot。

## 17. 非功能需求

### 17.1 可用性

1. Linux 用户只需安装 Docker Engine 和 DCNetLab。
2. macOS 用户通过自动管理的 Linux VM 运行数据面。
3. 正常流程全部通过 Web UI 完成。
4. 默认 Micro Profile 应在普通开发机上运行。
5. UI 应清晰显示 Applying、Running、Degraded、Failed 和 Stopped 状态。

### 17.2 性能

Micro Profile 目标：

```text
启动时间 <= 2 分钟
拓扑页面加载 <= 3 秒
普通操作响应 <= 1 秒
抓包元数据延迟 <= 1 秒
故障状态刷新 <= 2 秒
```

Standard Profile 目标：

```text
最多 50 个网络节点
最多 100 个 Workload
最多 200 条链路
最多 20 个并发 Traffic Scenario
最多 10 个并发 Capture Session
```

以上为本地开发机目标，不作为大规模生产性能承诺。

### 17.3 安全

1. UI 不允许执行任意 Shell。
2. Agent API 必须鉴权。
3. 所有高权限操作必须经过 Controller。
4. BPF Filter 必须校验。
5. 容器镜像支持 Allowlist。
6. 文件路径必须防止目录穿越。
7. PCAP 和日志必须设置访问权限。
8. 所有变更操作必须记录审计日志。
9. 默认只监听 localhost。
10. 远程访问模式必须显式启用认证。

### 17.4 可观测性

Controller 和 Agent 应暴露：

```text
API Request Count
Reconcile Duration
Apply Failure Count
Node Runtime State
BGP Session State
Route Count
Interface Traffic
Capture Drop Count
Traffic Scenario Metrics
Agent Health
```

第一版可以直接由 Controller 保存和展示，不强制部署独立 Prometheus。

### 17.5 可测试性

必须具备：

1. 模型单元测试。
2. IPAM 和 ASN 分配测试。
3. Topology Compiler 测试。
4. FRR 配置 Golden Test。
5. Runtime Driver 集成测试。
6. 物理网络连通性测试。
7. 故障收敛测试。
8. VXLAN 通信测试。
9. EIP SNAT/DNAT 测试。
10. Capture Decoder 测试。
11. UI 关键流程端到端测试。

## 18. 平台依赖要求

### 18.1 用户侧依赖

Linux：

```text
Docker Engine
DCNetLab
Browser
```

macOS：

```text
DCNetLab Launcher
Automatically Managed Linux VM
Browser
```

### 18.2 平台内部依赖

| 组件                | 是否必需                    |
| ----------------- | ----------------------- |
| Go                | 必需                      |
| Docker Engine API | 必需                      |
| Linux Netlink     | 必需                      |
| FRR               | 必需                      |
| AF_PACKET         | 必需                      |
| SQLite            | 必需                      |
| React             | 必需                      |
| Cytoscape.js      | 必需                      |
| Containerlab      | 第一版推荐                   |
| nftables          | EIP 和 Security Group 必需 |
| eBPF              | 可选增强                    |
| tcpdump           | 非必需                     |
| tshark            | 非必需                     |
| Wireshark         | 非必需                     |
| Kubernetes        | 非必需                     |

## 19. API 需求

系统至少提供以下 API 分组：

```text
/api/v1/labs
/api/v1/topologies
/api/v1/nodes
/api/v1/links
/api/v1/servers
/api/v1/workloads
/api/v1/traffic-scenarios
/api/v1/capture-sessions
/api/v1/fault-scenarios
/api/v1/vpcs
/api/v1/subnets
/api/v1/eips
/api/v1/plans
/api/v1/generations
/api/v1/operations
```

实时数据通过 WebSocket 推送：

```text
Topology State
Interface State
BGP State
Traffic Metrics
Packet Metadata
Operation Progress
Fault Events
```

## 20. 第一版关键用户流程

### 20.1 创建物理网络

```text
Create Lab
Select Micro Profile
Preview Plan
Apply
Wait for BGP Convergence
Run Connectivity Validation
Open Topology
```

### 20.2 部署真实业务

```text
Select Server
Deploy HTTP Server
Select Another Server
Deploy HTTP Client
Create Traffic Scenario
Observe RPS and Latency
```

### 20.3 抓包

```text
Select Link
Start Capture
Select Protocol Filter
Observe Packet List
Open Packet Detail
Download PCAPNG
```

### 20.4 故障实验

```text
Create Continuous HTTP Traffic
Start Multi-Point Capture
Inject Spine Link Failure
Observe BGP Convergence
Observe Request Failure
Observe Path Change
Generate Report
```

### 20.5 创建 VPC

```text
Create VPC
Create Subnet
Deploy Two Workloads on Different Servers
Attach Workloads to Subnet
Verify VXLAN Communication
Inspect Outer and Inner Packets
```

### 20.6 绑定 EIP

```text
Allocate EIP
Bind to Workload
Start External HTTP Request
Inspect VM Packet
Inspect VXLAN Packet
Inspect EGW SNAT Packet
Verify Return Traffic
```

## 21. 第一版验收标准

项目 V1 必须满足以下验收项：

1. Linux 上可以一条命令启动 Micro Profile。
2. macOS 上可以自动启动 Linux VM 并访问 Web UI。
3. 默认拓扑包含 DCGW、SuperSpine、Spine、Leaf 和 Server。
4. 所有网络设备运行真实 BGP。
5. Server 之间通过 Clos Fabric 通信。
6. UI 能查看节点、链路、接口、BGP 和路由状态。
7. UI 能新增 Pod、Leaf、Rack 和 Server。
8. 系统自动完成 ASN 和地址分配。
9. Server 可以运行真实 HTTP Server。
10. 另一台 Server 可以持续发送真实 HTTP 请求。
11. UI 能显示 RPS、成功率和时延。
12. UI 能从任意节点和接口启动抓包。
13. 抓包不依赖用户安装 tcpdump、tshark 或 Wireshark。
14. UI 能解析 Ethernet、IP、TCP、BGP、BFD 和 VXLAN。
15. UI 能保存和下载 PCAPNG。
16. UI 能注入链路 Down、设备 Stop、Delay 和 Packet Loss。
17. 故障时业务流量能够展示中断和恢复。
18. 系统能创建 VPC、Subnet、VNI 和 VXLAN。
19. 不同 Server 上同一 VPC 的 Workload 可以通信。
20. 系统能为 Workload 绑定 EIP。
21. Workload 可以通过 EIP 访问模拟公网服务。
22. UI 能展示 NAT 前后的报文变化。
23. Persistent Workload 重启后数据仍然存在。
24. 所有 Apply 操作具有 Plan 和状态反馈。
25. 系统支持恢复到上一个 Generation。

## 22. 版本规划

### V0.1：Runtime Prototype

交付：

```text
Micro Topology
FRR eBGP
Linux Data Plane
Basic Controller
One-Command Startup
```

### V0.2：Physical Fabric

交付：

```text
DCGW
SuperSpine
Spine
Leaf
Server
ECMP
BFD
Topology UI
```

### V0.3：Operations

交付：

```text
IPAM
ASN Allocation
Visual Expansion
Plan and Apply
Fault Injection
```

### V0.4：Server and Traffic

交付：

```text
Workload Container
Application Catalog
Persistent Volume
Traffic Scenario
Business Metrics
```

### V0.5：Native Capture

交付：

```text
AF_PACKET Capture
Packet Decoder
WebSocket Stream
Packet UI
PCAPNG
Multi-Point Capture
```

### V0.6：Virtual Network

交付：

```text
VPC
Subnet
VRF
VXLAN
L2VNI
L3VNI
Controller Programming
```

### V0.7：EIP

交付：

```text
EGW
SNAT
DNAT
Conntrack
External Network
Capture Along Path
```

### V1.0：Integrated Release

交付：

```text
Linux and macOS Installer
Stable UI
Rollback
Experiment Report
End-to-End Tests
Documentation
Example Labs
```

## 23. 后续扩展

V1 之后可扩展：

```text
BGP EVPN
Route Reflector
Security Group
NAT Gateway
Public Load Balancer
Distributed Gateway
Distributed SNAT
IPv6
Multi-IDC
Inter-Region
SR-MPLS
SRv6
SONiC Backend
P4/BMv2 Backend
Topology Import
Configuration Import
Experiment Sharing
Teaching Mode
Automated Root Cause Analysis
```

## 24. 风险与约束

### 24.1 macOS 数据面限制

macOS 缺少 Linux Network Namespace、veth、VRF 和 nftables，因此必须运行 Linux VM。平台需要隐藏 VM 管理细节，否则会影响一键启动体验。

### 24.2 容器权限

创建 veth、VRF、VXLAN、AF_PACKET 和 FRR 需要较高权限。Agent 必须限制暴露面，避免将完整宿主机权限直接开放给 UI。

### 24.3 Containerlab 增量更新限制

第一版可以采用全量重建，但需要避免拓扑变更导致 Persistent Workload 数据丢失。持久化数据和网络生命周期必须解耦。

### 24.4 抓包性能

全量 Payload 和高 PPS 抓包会显著消耗 CPU 和内存。第一版必须限制抓包时长、大小和并发数量，并优先推送元数据。

### 24.5 协议解析复杂度

完整实现 Wireshark 级别协议解析不现实。系统应优先支持数据中心网络最常用的协议，并允许后续以插件方式扩展 Decoder。

### 24.6 资源规模

本系统定位为单机实验平台，不承诺在一台普通笔记本上模拟数千台设备。架构应支持扩展，但第一版优先保证 50 个网络节点以内的稳定性。

## 25. 完成定义

一个功能只有在同时满足以下条件时才视为完成：

1. API 和数据模型已实现。
2. UI 可以完成主要操作。
3. Runtime 能够正确执行。
4. 状态可以被 Observer 采集。
5. 失败场景有明确错误信息。
6. 操作具备审计记录。
7. 有单元测试或集成测试。
8. 有验收用例。
9. 不依赖用户手工执行命令。
10. 相关文档已经更新。

## 26. 项目成功标准

DCNetLab 的成功不以支持最多协议或最多设备为判断标准，而以是否形成完整、可复现的网络实验闭环为核心标准：

```text
创建拓扑
部署真实网络
运行真实服务
产生真实流量
注入网络故障
观察控制平面
观察业务影响
进行 UI 抓包
分析逐跳报文
恢复和复现实验
```

当用户可以在不手工编写复杂配置、不依赖外部抓包工具、不频繁进入命令行的情况下完成上述流程，即认为项目达到了第一阶段目标。
