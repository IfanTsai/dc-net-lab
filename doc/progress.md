# DCNetLab 项目进度记录

> 本文档跟踪实际开发进度，按设计文档（[design.md](design.md)）第 26 节的迭代计划组织。每次迭代完成或有重要进展时更新。

## 总览

| 迭代 | 内容 | 状态 |
| --- | --- | --- |
| Iteration 0 | 运行时验证：Controller 骨架、Vue 骨架、两节点拓扑 | ✅ 已完成 |
| Iteration 1 | 物理网络闭环：Profile、IPAM、ASN、FRR 编译、Plan/Apply、Topology UI | ✅ 已完成，含真实部署验证 |
| Iteration 2 | Server Program:Compute Server Container、Server Agent、trafficgen | ✅ 已完成，含 Package 制品部署 |
| Iteration 3 | Daemon Framework | 🚧 进行中（Auto Start / Restart Policy / 存活 + 就绪检查 / 启动顺序已落地；剩就绪门控排序、配置渲染 + Provider、Daemon UI，留到接 Pingmesh 前再做） |
| Iteration 4 | Traffic | ✅ 已完成，含真实环境验证 |
| Iteration 5 | 原生抓包（AF_PACKET） | ⬜ 未开始 |
| Iteration 6 | 故障实验 | ✅ 已完成，含真实环境验证（Link Down/Node Stop/Delay/Loss/Rate Limit/故障恢复/Traffic 联动均达成，Capture 联动待 It. 5） |
| Iteration 7 | 扩容和 Generation Rollback | ⬜ 未开始（Generation 快照已就绪） |
| Iteration 8 | Pingmesh 集成 | ⬜ 未开始 |
| Iteration 9 | 虚拟网络（VPC/VXLAN） | ⬜ 未开始 |
| Iteration 10 | EIP | ⬜ 未开始 |

在迭代计划之外，已提前实现设计 §13 的 Observer 状态采集（含 WebSocket 实时推送）与拓扑页 Web 终端、DC/设备启停控制。

## 已实现能力

### 声明式控制闭环（Plan/Apply）

Create Lab（Micro/Standard Profile）→ Plan（资源分配预览）→ Apply（编译 + 部署 + 校验）→ Generation 快照 → UI 实时观测。

- `internal/model` — 零依赖共享资源模型（Lab/Node/Link/Plan/Operation），统一 Phase 状态机，Generation/ObservedGeneration 元数据。
- `internal/allocator/ipam` — 地址池分配器：顺序分配、回收复用、重启恢复（Restore），含单元测试。
- `internal/allocator/asn` — 按角色分段的 ASN 分配器；leaf 按机柜计算、server 固定 65000，不走范围分配器。
- `internal/topology` — Profile → 完整 Clos 拓扑构建：External→DCEdge→SuperSpine→Spine→Leaf（MLAG 对）→Server；路由器间 /31 互联、/32 Loopback；含唯一性/范围测试。
- `internal/compiler` — 资源模型 → FRR 配置 + Containerlab 拓扑 YAML，全部为编译产物（模板不计算资源）；Golden 测试守护（`make golden` 更新基线）。
- `internal/operation` — 异步 Operation 执行器，分步进度持久化，失败带结构化错误；onDone 收尾先于落库 Succeeded，轮询端读到的一定是一致状态。
- `internal/runtime` — Runtime Driver 接口：`containerlab`（PATH 检测到二进制自动启用）/ `noop`（仅生成产物）；产物路径防目录穿越。
- Generation 快照保留最近 10 个，为后续 Rollback（Iteration 7）预留。

### 后端架构（Kratos + protobuf）

- **protobuf 单一事实来源**：`api/dcnetlab/v1/dcnetlab.proto` 定义全部消息与 service，`google.api.http` 注解声明 REST 路由；`make api`（buf generate）生成消息/gRPC/Kratos HTTP 三份代码到 `pb/`（勿手改）；`google/api` 依赖经 BSR 锁定（`buf.lock`），仓库不落 third_party。REST 监听 8080（含 Web UI SPA 托管与 index.html fallback），gRPC 9090（`--grpc-listen` 可关）。
- **分层严格单向**（kratos-layout）：service（pb ↔ model 转换、错误映射）→ biz（usecase 编排，按 lab/plan/topology/operation/power/terminal 模块拆分，每模块定义自己的窄 Repo 接口）→ data（SQLite 全部落此，与 biz 同构拆分）；每层同名文件持有该层 wire ProviderSet，`cmd/controller/wire.go` 汇总生成注入代码（`make wire`）。
- **SQLite 持久化**（modernc.org 纯 Go 驱动）：labs/nodes/links/plans/operations/generations/allocations，事务性拓扑替换。
- HTTP 定制：宽松请求解码器（bodyless POST 无 Content-Type 时按 JSON 处理）；protojson 映射约定（list 返回包装对象、int64 序列化为字符串、Kratos 结构化错误体）。
- 工具链版本全部固定在 `scripts/init-tools.sh`（`make init`），无需系统 protoc。

### 地址与 ASN 规划

- **内网统一从 10.0.0.0/8 划分**：fabricP2P `10.0.0.0/16`（/31）、loopback `10.1.0.0/16`（/32）、vtep `10.2.0.0/16`、management `10.3.0.0/16`、serverVlan `10.100.0.0/16`（/24 每机柜，rack N → `10.100.N.0/24`）；上半段 `10.128.0.0/9` 整体保留给 overlay workload；`10.4–10.99`、`10.101–10.127` 预留；public `198.18.0.0/15`（RFC 2544，刻意在内网段外模拟公网）。
- **ASN**：external 64500 起、dc-edge 64600 起、superspine 65100 起、spine 65200 起；server 固定 65000（well-known），leaf ASN = 4200080000 + 全局 rack 序号（MLAG 对共享，机柜编号跨 pod 递增避免冲突）。
- **接入层路由聚合**：leaf 对 server 只 `default-originate` 一条默认路由 + 出方向 prefix-list 过滤（fabric 明细不出接入层），server FIB 仅 3 条（本机柜直连 /24、静态默认经 VRRP 虚 IP 主用、BGP 默认经两台 leaf 物理 IP ECMP 备用）；fabric 内部刻意保持明细不聚合——聚合会在链路故障时产生黑洞。守护测试 `TestLeafAdvertisesOnlyDefaultToServers`。

### 机柜级 MLAG 接入层

- **Rack = MLAG leaf 对 + 服务器**：每机柜两台 leaf（`leaf-a`/`leaf-b`）互为 MLAG 对，之间 peer link（trunk VLAN1000）；到 server 的端口为 access VLAN1000。
- **VRRP 多活网关**：两台 leaf 的 vlan1000 SVI 配不同物理 IP（`.2`/`.3`）+ 共享虚拟网关 IP（`.1`）与标准 VRRP 虚 MAC（`00:00:5e:00:01:{VRID}`），leaf-a 优先级 200 抢占；FRR vrrpd + 预建 macvlan 接口。
- **Server bond 双归 + BGP**：server `bond0`（active-backup）成员口分别接两台 leaf；运行 FRR 与两台 leaf 建 eBGP——**邻居指向 leaf 的 vlanif 物理 IP，而非 VRRP 虚 IP**（golden 测试守护此约束）；leaf 侧 `SERVERS` peer-group + `bgp listen range <rack /24>`，MLAG 对之间跑 iBGP。
- **MLAG 为仿真语义**：FRR 无 MC-LAG 控制面，L2 冗余由双 access 口 + peer link 提供，网关多活由 VRRP 提供（无跨机箱 LACP）。
- 拓扑规模：Micro = 9 节点 / 13 链路；Standard = 25 节点（2 pod × 2 rack）。

### Runtime 部署与校验

- **编译产物即全部状态**：leaf 容器 exec 构建 vlan_filtering 网桥、vlan1000 SVI、VRRP macvlan；server exec 构建 bond；链路模型带 `kind`（fabric / server-access / mlag-peer），L2 链路无端点地址。
- **管理网默认路由治理**：所有非 server 节点 exec 移除 containerlab 注入的 `default via <mgmt-gw> dev eth0`，server 用 `ip route replace` 显式接管——否则 BGP 撤路由后流量会静默 fallback 到管理网逃逸，"设备停止"的故障隔离语义失真。
- **Apply 自带两步校验**（noop 运行时自动跳过）：`ValidateControlPlane` 按模型推导每节点期望 BGP 会话数并轮询到全部 Established；`ValidateDataPlane` 每台 server ping VRRP 网关 + ping 下一台 server（跨机柜穿越 fabric）。
- **真实环境验证结论**（Micro，9 容器）：26 条 BGP 会话全部 Established；VRRP 自动选主与秒级故障切换（macvlan down → 对端接管、VIP 不中断、恢复后抢占回）；spine 到机柜 /24 双下一跳 ECMP。

### macOS 适配（OrbStack 转发运行时）

containerlab 依赖 Linux 内核（netlink、网络命名空间），Darwin 无法本地部署。适配思路是**把整个运行入口转发进一台 OrbStack Linux 虚机**，而非在 macOS 上模拟（原理与图示详见 [macos.md](macos.md)）：

- **透明转发**（`scripts/dcnetlab`）：Darwin 且运行时非 `noop` 时，`up`/`down`/`status`/`logs`/`edge-image` 整条命令 re-exec 进 OrbStack 机器执行。OrbStack 把 `/Users` 以相同路径挂载进虚机（virtiofs），repo、`.run` 目录与工作目录零改动携带；`DCNETLAB_LISTEN` 在虚机内改绑 `0.0.0.0`，并经 `DCNETLAB_ADVERTISE` 用 `<machine>.orb.local` 主机名回显 URL，mac 浏览器直连。tty 场景下 stdin 接 `/dev/null`、stdout 走管道，规避 `orb` 对终端 raw mode 的干扰（清屏 / 阶梯行）。
- **幂等装机**（`scripts/orb-setup`）：创建 Ubuntu 机器并安装 Docker（含把登录用户加进 docker 组）、containerlab、Go、Node，全部检测后跳过、可重复执行；另装 docker.service drop-in——OrbStack 的 `/Users` virtiofs 在 systemd 视野之外异步挂载，docker 若先于共享就绪重启容器，源文件缺失的单文件 bind mount 会被静默建成空目录（agent 二进制从此挂空、自愈失效），drop-in 用有界 `ExecStartPre`（最长 60s）等挂载点就绪再放行 docker。
- **双平台共享 node_modules**：mac 与虚机共用同一份 `web/node_modules`，但 rollup/esbuild 的原生二进制按平台走 optional package，`ensure_web_natives` 只补装缺失平台的原生包并合并进树，避免整树 `npm install` 互相剪除对方平台。
- **降级链与报错指引**：未装 OrbStack / 机器不存在 / 设 `DCNETLAB_RUNTIME=noop` 时保持本地运行、运行时降级 `noop`（仅生成产物），脚本打印安装 hint；biz 层把 `ErrNotSupported` 翻译为带解法的错误（装 containerlab，或 macOS 上 `make orb-setup` + `make down && make up`），不再裸露 `operation not supported by this runtime driver`。

### DC/设备启停

- proto 4 个 RPC:`StartLab`/`StopLab`（异步 Operation）、`StartNode`/`StopNode`（同步返回 Node）。
- **冻结语义**：统一 `docker pause`/`unpause` —— 保留 veth 接线与 exec 注入配置，停止即设备静默（邻居 BGP hold 超时、VRRP 切换，与真实宕机一致），启动即秒级解冻（实测 25 容器 1s）。**不能用 `docker stop/start`**：实测 stop 后 veth 与注入配置全部丢失，start 回来只剩管理口 eth0。销毁/重建走 DeleteLab/ApplyPlan。
- **幂等收敛**：driver 按 docker 实时状态过滤（StopNodes 只 pause 运行中的、StartNodes 只 unpause 已暂停的），无论 DB 与容器状态如何漂移，启停操作都把实际状态收敛到目标状态并刷新 phase。

### Observer 状态采集（设计 §13）

周期采集已部署 lab 的**实际状态**并回填，UI 显示真实状态而非期望状态。

- **两档采集**（`internal/observer`，Kratos transport.Server 生命周期）：2s 快速采集（一条 `docker ps -a` 批量拿全部容器状态）；6s 深度采集（并发信号量 8，对每个 running 容器单次 exec 组合脚本一次拿到接口状态、BGP established/configured、路由总数、VRRP 角色）。设计 §13 的 2/3/5s 分表合并为两档，减少 exec 压力；noop 运行时整体跳过。
- **接口统计只覆盖模拟对象**：拓扑链路端点 + 建模的逻辑接口（leaf 的 vlanif、server 的 bond0），并逐接口回填 up/down；br0、VRRP macvlan、管理口 eth0 等实现层接口不计入，仿真视图与拓扑模型口径一致。VRRP 网关角色（Master/Backup）单独采集（`show vrrp json`），避免把 backup 侧 macvlan 的 protodown 误读为接口故障。
- **管理视角按需查询**：`GET /api/v1/labs/{labId}/nodes/{nodeId}/runtime`（`biz.RuntimeUsecase`）返回底层容器状态与全部内核接口（名称/状态/MAC/地址，含实现层管道），只在打开时 exec，不进周期采集。
- **漂移自动纠正**（有意突破"Observer 只写 Observed"原则）：容器实际状态映射 node phase（running→Running / paused→Stopped / 其它→Failed），lab phase 由设备推导（全开 Running / 全停 Stopped / 混合 Degraded），Planning 等过渡态不碰；手动 `docker pause`、宿主机重启等漂移在一个采集周期内自动收敛。
- **WebSocket 推送**：`GET /ws/v1/labs/{labId}/topology`，连接即发最新快照、每次采集全量推送；慢消费者跳帧不阻塞。`lastObserved` 语义为"数据最后变化时间"——仅观测值变化时落库，稳态零写放大。
- **Agent 可达性上报**（治"假 Running 无信号"）：深度巡检的 agent 探测结果落入 `NodeStatus.AgentState`（`Up`/`Down`，仅 running 的 server 有值），随节点 REST 与 WebSocket 推送透出；自愈成功当轮复测即回 `Up`，无假 Down 窗口。UI 三处消费：拓扑图 server 角标在 agent 不可达时转橙、节点抽屉实时观测区显示"Node agent 可达/不可达"（不可达附恢复提示）、Programs 页顶部对 agent 不可达的 server 显示警告条。自愈失败的持久 Down（如虚机重启后挂载丢失）由此对用户可见，不再只能靠操作报错发现。
- 单测覆盖漂移纠正、稳态零写、未部署跳过、订阅广播与解析函数、agent 探测 Down/自愈复活 Up 与广播携带。

### Web UI(Vue 3 + TypeScript + Pinia + Element Plus + Cytoscape.js)

- **三页面**：Labs（创建/Plan 预览/Apply/删除）、Topology、Operations（步骤进度，2s 轮询）；vue-i18n 国际化（中/英，默认跟随系统，可切换）。
- **拓扑画布**：分层 Clos 布局 + Pod/机柜 compound 分组框；按角色语义化设备图标（内联 SVG），右上角实时状态角标（绿=运行且 BGP 全建立且 agent 可达 / 橙=BGP 未收敛 / 紫=仅 server 的管理失联（node-agent 不可达，数据面正常）/ 灰=已停止 / 红=异常，无角标=尚未观测），画布左下角图例含状态灯行（逐项 hover 提示判定条件，末尾 `?` 弹出完整规则说明）；所有链路统一细的近黑灰（`#303133`）静态实线，选中设备蓝色边框并联动高亮直连链路，停止设备及其链路置灰；观测推送走**增量更新**（结构未变时原地刷新图标/类，不重置用户缩放/拖拽）；节点抽屉分「模拟视角 / 运行时」两个标签页——模拟视角展示配置 + 实时观测（运行状态、BGP 会话 x/y、路由数、接口 up x/y、VRRP 网关角色、最后观测时间）、接口表（拓扑链路 + vlanif/bond0，逐接口 up/down，本端/对端地址）与 BGP 配置表（`GET /api/v1/labs/{labId}/nodes/{nodeId}/bgp`，`TopologyUsecase.GetNodeBGP` 直接复用 `frr.BuildRouterConfigs`，展示邻居/远端 AS/对端与 leaf 的 SERVERS 动态监听组，与下发的 frr.conf 永不漂移），路由三层按真实运维逻辑分标签页按需拉取——**BGP**（`/nodes/{id}/bgp-table`，`show ip bgp json` 的 Loc-RIB 全部候选路径，best/multi/iBGP 标记 + AS-Path + 下一跳设备名，best path 选择前的视角）、**RIB**（`/nodes/{id}/routes`，zebra 路由表，前缀/协议/AD/Metric/ECMP 下一跳）、**FIB**（`/nodes/{id}/fib`，内核 `ip -j route show table all`，对齐真实交换机的两层：main 表 LPM 转发条目 + local 表本机条目（标 `local`，等价 ASIC host/punt 表），broadcast/multicast/127/8/IPv6 等内核噪声过滤），三层条目数呈漏斗递减，可直观对照 best path 选择与 RIB→FIB 下装；运行时标签页按需拉取底层容器状态与全部内核接口（含 br0、VRRP macvlan、eth0 等实现细节）；抽屉非模态（开着也能继续操作画布，点画布空白自动收回）。
- **Web 终端**：双击设备节点弹出可拖拽/缩放/最大化的悬浮多标签 xterm.js 终端（交换机/路由器进 vtysh，server 进 bash），经 WebSocket 对接容器内 PTY（creack/pty + `docker exec -it`）；最小化停靠为右下角胶囊（会话数 + hover 列表 + 后台活动指示点）。
- **启停控制**：页头一键启停数据中心按钮 + 节点抽屉设备启停按钮。
- 前端加入 Playwright（devDependency），用于 UI 验证截图与后续 E2E。

### Server Program 框架（设计 §9.6–10.6、§15.1，Iteration 2）

用户可以从 UI 在 server 上部署并启停真实程序（Iteration 2 退出标准已达成）。

- **`dcnetlab-trafficgen`**（`serverapps/trafficgen` + `serverapps/internal/trafficgen`）：单二进制六模式（http/tcp/udp 的 server/client），统一 JSON 结构化打点（total/failed/rate/latency 每 5s 一行），为后续 Traffic 复用打底；client 断连自动重拨。
- **`dcnetlab-node-agent`**（`serverapps/node-agent` + `serverapps/internal/agent`）：每台 server 容器内的进程监督器，gRPC（`api/nodeagent/v1`，默认 `:50061`，管理网可达）提供 InstallPackage/Install/Start/Stop/Remove/List/TailLogs；进程组管理（Setpgid + SIGTERM→SIGKILL）、RestartPolicy（Never/OnFailure/Always，指数退避封顶 15s）、meta.json 持久化（agent 重启杀残留进程并恢复期望态）、日志落盘 + 5MB 轮转；按设计**无任意命令执行接口**，只能运行制品库中带校验和的包。
- **交付形态**：`make build` 产出静态二进制（CGO off，跑在 Alpine 基础的 FRR 容器里），编译器给 server 节点注入单文件 `binds`（仅 agent，语义上等价于 OS 预装的 node agent；`--bin-dir` 默认 controller 同目录）+ exec 后台拉起；不构建独立镜像。
- **Program 资源**（`internal/model/program.go` + biz/data/service 分层 + `pb` API）：`/api/v1/labs/{labId}/programs` CRUD + start/stop/upgrade + logs；desired state 存 controller SQLite，**独立于网络 Plan/Apply**；agent 地址运行时经 `docker inspect` 解析（`Driver.NodeAddress`），不依赖模型中的管理 IP。
- **重部署自动恢复**：apply 新增 `RestorePrograms` 步骤——`--reconfigure` 摧毁容器后先向新 agent 重装包、再按 desired state 重装重启程序（等待 agent 就绪最长 30s）。关键坑：**每次 plan 重建节点拿到新 ID**，程序以 ServerName（跨代次稳定）重绑定并回填新 ServerID。已实测：重部署后程序自动回到 Running，跨 fabric 流量秒级恢复。
- **Programs UI**：新页面（列表 3s 轮询 / 创建对话框选 server+软件包+版本+参数+重启策略 / 启停删除升级 / 日志抽屉）；未部署实验禁用操作并提示。
- **真实环境验收**（dc1，25 容器）：rack-1 server 起 `http-server`、pod-2 rack-3 server 起 `http-client --interval 500ms` 跨 fabric 请求，双端日志 2 req/s、0 失败、延迟约 0.5ms；停止/启动/删除、重部署恢复全部验证通过。

### Package 制品部署流程（设计 §9.6 Program Package，Iteration 2.5）

把"业务程序如何到达 server"抽象成真实数据中心的部署链路：制品入库 → 内部源分发 → 校验安装 → 运行，替代此前内置应用直接 bind mount 的交付捷径。

- **Package 资源**（`internal/model/package.go` + biz/data/service 分层）：tar.gz 制品 + 根目录 `manifest.json`（name/version/entrypoint/description）为唯一身份来源；SQLite 存元数据（`UNIQUE(name, version)`），payload 落盘 `data/packages/<name>/<version>.tar.gz`；`/api/v1/packages` 提供上传（bytes/base64）、列表（name 升序 + 版本降序）、删除。
- **内置包同链路**：Controller 启动时把构建产物 trafficgen 打包注册为 builtin 包（`trafficgen@0.1.0`，不可删除；名字保留）；同版本二进制内容变化（开发迭代）自动替换 payload 并更新校验和，agent 端按 sha 比对触发重装。
- **拉模型分发**：Controller 在 `--repo-listen`（默认 `0.0.0.0:50062`）暴露只读包仓库（`GET /packages/<name>/<version>`）；`InstallPackage` 指令携带 URL + sha256，URL 主机为该 server 容器管理网网关（`Driver.NodeGateway`，经 docker inspect 发现），agent 主动下载、流式校验 sha、防路径逃逸解包（拒绝 `..`/绝对路径/链接）、原子 rename 激活，同版本同 sha 幂等跳过。
- **多版本 + 升级回滚**：agent 包存储按 `packages/<name>/<version>/` 共存（GC 保留最近 3 版 + 程序引用的版本）；`POST .../programs/{id}/upgrade {version}` 切换引用并重启（先停旧进程再装新定义），回滚是同一操作，本地已有版本**不重新下载**（实测 agent 安装事件数不变）。
- **Program 引用 package@version**：`ProgramSpec` 以 `packageName + packageVersion + entrypoint（自包 denormalize）+ args` 取代原 mode 字段；存量 mode 程序在数据层启动迁移中自动转换为 `trafficgen@0.1.0` 引用（mode 折叠为第一个参数）。
- **代码结构隔离**：server 内运行的程序移入顶层 `serverapps/` 子树（`serverapps/{node-agent,trafficgen}` + `serverapps/internal/{agent,trafficgen}`），Go internal 规则硬性阻止 controller 引用其实现；两侧共享的线上契约常量（端口/状态/重启策略）抽到 `internal/agentapi`。
- **命名定稿**：内置流量应用定名 `trafficgen`（原 lab-app）、容器内 agent 定名 `node-agent`（原 server-agent），目录/二进制/proto（`api/nodeagent/v1`，服务 `NodeAgent`）/builtin 包名全链路一致；存量程序引用与旧 builtin 包行经数据层启动迁移自动更新。
- **容器内 pkg CLI**：`pkg list|list <name>|install <name>[@<ver>]|remove <name>[@<ver>]`——登录 server 终端即可像用发行版包管理器一样查看制品库与装卸包，对应真实服务器上的 `yum/apt` 运维体验。CLI 是独立的多调用二进制 `node-cli`（busybox 式，`pkg` 为其软链，按 `argv[0]` 分发子命令，`node-cli pkg ...` 等价）；daemon 与 CLI 在容器内以裸名挂载（`/opt/dcnetlab/bin/{node-agent,node-cli}`，进程名即 `node-agent`），部署时软链进 `/usr/local/bin`。包仓库新增 JSON index 端点（`GET /packages`、`GET /packages/<name>`，按版本降序）；agent 新增 `RemovePackage` RPC（被程序引用的版本拒删，回复携带引用它的程序名）；`pkg remove` 一个版本都没删掉时按错误处理（退出码 1，报错指明 `<pkg>@<ver> is in use by program <name>`），部分删除成功时保留版本以 `kept:` 行说明原因；安装经本机 daemon 的 gRPC 走同一条下载/校验/解包链路（保持包存储单写者）。repo 地址自动发现：`--repo`/`DCNETLAB_REPO` → daemon 在每次安装时记下的 `repo.url` → eth0 网段首地址 + 默认端口 50062。
- **Packages UI**：新"软件包"页面（列表含版本/入口/大小/SHA-256/builtin 标记、tar.gz 上传、删除保护）；Programs 创建对话框改为选包 + 版本（trafficgen 保留模式下拉作为参数预设），行操作新增"升级"（版本切换对话框）。
- **UI 直装包到服务器**：包不一定要以 Program 运行——工具类静态 binary 的部署诉求走"安装到服务器"行操作（`POST /api/v1/labs/{labId}/packages/{name}/{version}/install`，选实验 + 多选 server，空选即全部），Controller 逐台调 agent `InstallPackage`（幂等），逐台返回成败（部分失败不中断，对话框内逐行展示）；对应 `apt install` 装完即用、无需 service unit 的场景，装完终端里直接可执行。
- **真实环境验收**（dc1）：上传 `demo@1.0.0/2.0.0`（shell 服务）→ 部署运行 → 升级 2.0.0（日志切 v2、双版本目录共存）→ 回滚 1.0.0（无重复下载）→ 重部署后 trafficgen 双程序 + demo 全部自动恢复 Running、流量归零丢包；builtin 删除被拒。

### Program 的 systemd 化语义（类型 / 开机自启 / 崩溃自愈）

参考 systemd service 语义补齐"真实环境部署的不一定是 daemon"这块拼图，并把 agent 的两处生命周期缺口（graceful 重启误停程序、容器重启后 agent 无人拉起）补上。

- **程序类型**（systemd `Type=`）：`ProgramSpec` 增加 `type`——`simple`（常驻服务，默认）/ `oneshot`（一次性任务，跑完即止）。oneshot 正常退出进入新终态 `Exited`（区别于 Stopped/Failed，对应 systemd oneshot 的 inactive/dead）；oneshot 拒绝 `Always` 重启策略（与 systemd 行为一致，agent 与 biz 双侧校验）。UI 创建对话框用类型单选联动重启策略选项，oneshot 的启动按钮显示"运行"。
- **开机自启**（systemd `systemctl enable`）：`ProgramSpec` 增加 `autoStart`。启用的程序在每次"开机"时自动启动，无论之前是否在跑：agent 进程启动（`NewManager` 恢复）与重部署（`RestorePrograms`）都是开机路径。oneshot 的 Start 语义是"运行一次"（`systemctl start` 于 oneshot unit）：controller 侧 desired 即刻落回 Stopped，只有 autoStart 才会让它在下次开机/重部署重跑。
- **graceful shutdown 修复**：原 `Manager.Shutdown` 借道 `Stop` 把每个程序的 desired 持久化成 Stopped，导致 agent 优雅重启后所有程序被"恢复"为停止态——把"supervisor 要退出"和"用户想停程序"两个语义混在了一起。现拆出 `terminate`（只杀进程组、不动 desired），Shutdown 走 terminate；systemd 重启自身同样不会 disable 任何服务。
- **agent 崩溃自愈**：Observer 深度巡检（6s）对 running 状态的 server 容器探测 agent 端口（500ms TCP dial），拒连即通过 `Driver.Exec` 重放启动脚本拉起 agent——脚本收敛为 `agentapi.StartAgentScript` 单一来源，部署时的 containerlab exec 与自愈路径共用，杜绝漂移。复活的 agent 从本地 `meta.json` 恢复 desired Running 与 autoStart 的程序，自愈即"服务器开机"。覆盖场景：agent 被 OOM/误杀、宿主机挂起唤醒、容器被平台外重启（此时数据面 veth 仍需重部署恢复，但包/程序管理即刻可用）。
- **接线**：`api/nodeagent/v1` 与 `api/dcnetlab/v1` 的 `ProgramSpec` 增加 `type`/`auto_start`；存量程序 type 为空视同 simple，无需迁移。agent 新增单测：graceful shutdown 后恢复 Running、autoStart 冷启动拉起、oneshot 全生命周期（Exited、不重跑、Always 拒绝）。

### Program 存活检查（Iteration 3，Liveness Probe）

参考 systemd 健康检查 / k8s liveness probe，为 Program 补上"进程活着但不干活"这块语义——探测失败即按 `RestartPolicy` 重启，让 Running 状态从"进程在"升级为"服务可用"。Iteration 3 的健康检查交付项达成，Auto Start / Restart Policy 已在上一轮 systemd 化落地，本轮补齐存活探测。

- **三种探测**（`serverapps/internal/agent/health.go`）：`process`（被监督进程存在，`kill -0`，`EPERM` 视为存活）/ `tcp`（回环端口接受连接）/ `http`（回环 GET 返回 2xx/3xx），探测目标固定容器内 `127.0.0.1`；时序默认 interval 10 s（下限 1 s）、timeout 1 s（钳到 interval 之下保证同一时刻至多一个探测在飞）、failureThreshold 3。`normalise` 纯函数补默认 + 校验，表驱动测试。
- **监督语义**：配了 `livenessCheck` 的程序在 `startLocked` 里另起一个 goroutine，与监督循环共享 context（跨重启存活）；连续失败达阈值即向进程组发 SIGTERM——探测本身不重启，只"制造一次退出"，交由既有监督循环按 `RestartPolicy` 决定拉起（故 `Never` 下探测失败进 `Failed`）。进程不在运行或换了新 PID 时失败计数清零，避免对退避窗口空 PID 误杀；健康值只在运行时被探测结论覆盖，进程一停回落 `Unknown`。spec 随 `meta.json` 持久化，agent 重启恢复监督即恢复探测。
- **健康状态**：`ProgramStatus.Health` 为 `Unknown`（无检查 / 尚未探测）/ `Healthy` / `Unhealthy`，逐层透出（agent → `ProgramInfo.health` → controller model → API → UI）。
- **全链路接线**：`agentapi` 增加探测类型与健康态常量；`api/nodeagent/v1` 与 `api/dcnetlab/v1` 的 `ProgramSpec` 增加 `HealthCheck liveness_check`、状态增加 `health`；biz 层 `validateLivenessCheck` 在下发前挡掉非法类型 / 缺端口；data/service 双向转换（秒 ↔ `time.Duration`）。Programs 页创建对话框加"存活检查"配置（类型 / 端口 / 路径 / 周期 / 失败阈值），列表 State 列旁显示健康角标；容器内 `program deploy` 加 `--liveness*` 一组标志、`program list` 加 HEALTH 列，与 UI 对齐。
- **测试**：agent 侧覆盖 normalise 默认与校验、tcp/http/process 探测命中与失败、存活程序标 Healthy 且不重启、卡死程序（进程在但不监听）被探测重启；biz 侧覆盖非法探测拒绝与合法探测持久化。

### Program 就绪检查与启动顺序（Iteration 3，Readiness + StartupOrder）

在存活检查之上补齐设计 §10.7 的就绪探测与 §10.8 的启动顺序：就绪只报告"是否可服务"、不重启；启动顺序让"配置写入器先于依赖它的守护进程"这类关系在开机 / 重部署时成立。就绪探测复用存活的探测器（`process` / `tcp` / `http`，同一 `HealthCheck`）。

- **就绪探测**：`ProgramSpec.ReadinessCheck` 配了就起 `monitorReadiness` goroutine，语义同存活但**从不重启**——只把每次探测结论写进 `ready`（运行时被覆盖，进程一停回落 false）；无就绪检查的程序一进 Running 即 `ready`（对应 `Type=simple` 无就绪门）。`ProgramStatus.Ready` 逐层透出到 UI。
- **启动顺序**：`ProgramSpec.StartupOrder`（默认 0）。agent 的 `NewManager` 改为两段——先加载全部定义（并杀上一世孤儿），再把要启动的（desired running 或 autoStart）按 `StartupOrder` 升序（同序按名称）依次 `startLocked`；Controller 的 `RestorePrograms` 同样先按 `StartupOrder` 排序再逐个下发，重部署与 agent 开机路径口径一致。
- **本轮范围界定**：只做启动**顺序**（低序先于高序被启动），**尚未做**"等前序 ready 再启后序"的就绪门控——就绪门控涉及开机时延与门控归属（agent 侧 vs controller 侧）的取舍，作为紧接的后续项单列；就绪状态已就位，正是门控的消费信号。
- **全链路接线**：两份 `ProgramSpec` 增加 `readiness_check` / `startup_order`、状态增加 `ready`；biz 层校验器泛化为 `validateHealthCheck`（存活 / 就绪共用）；data/service 双向透传。Programs 页创建对话框加"就绪检查"配置与"启动顺序"输入，列表 State 列加就绪角标、Type 列显示启动顺序；`program deploy` 加 `--readiness*` / `--startup-order`、`program list` 加 READY 列。
- **测试**：agent 侧覆盖就绪程序报告 ready 且端口关闭后回落、无检查程序运行即就绪、开机按 `StartupOrder` 顺序拉起（`StartedAt` 递增）；biz 侧覆盖就绪校验与顺序持久化。

### Program 批量运维（创建即运行 / 批量启停删除）

补齐"批量部署却要逐个运行"的交互断层：批量创建的 N 个 Program 资源可一次拉起、事后可按选择集批量管理。

- **创建即运行**：`CreateProgramRequest.start`——创建对话框"创建后立即启动"开关（默认开），simple 程序 desired 落 Running（重部署随之恢复），oneshot 语义为"跑一次"、desired 保持 Stopped；已部署 lab 上装完即启，失败不中断创建、写入程序 status。
- **批量操作 API**：`POST /labs/{id}/programs/batch`（`op` = start/stop/delete + `ids`），复用单个操作的全部校验（跨 lab id 被拒），逐个 best-effort、回复逐条 results——与 `CreateProgram`/`InstallPackageOnServers` 的既有风格一致。
- **列表多选**：表格勾选列 + 选中后浮现批量操作栏（启动 / 停止 / 删除，删除带确认框）；部分失败逐条 warning。勾选经 `row-key` + `reserve-selection` 在 3 s 轮询刷新下保持（否则轮询一到勾选即被清空），删除命中选中行与切换 lab 时主动清空选择集，防"幽灵勾选"。
- **测试**：biz 覆盖创建即运行（simple desired Running / oneshot 跑一次不留 desired）、批量三操作、含未知 id 的 best-effort 与非法 op 拒绝。

### 批量交付与节点视角（范围选择 / 库存 / program CLI）

把"往一台 server 上装东西"扩展成面向机群的交付操作，并补齐"这台机器上到底有什么"的节点视角。

- **范围选择器**：新组件 `ServerScopeSelect`（el-cascader，pod → rack → server 树，勾选分支即整组选中，值为叶子 server ID 列表），Packages 安装对话框与 Programs 创建对话框共用；安装侧保留"空选即全部"（dc 粒度），部署侧至少选一台。
- **批量部署程序**：`CreateProgram` 请求 `server_id` 升级为 `repeated server_ids`——同一份定义在多台 server 各建一个 Program 资源（类 DaemonSet 语义），回复携带 programs + 逐台 failures（重名、非 server 目标等不中断其余目标；全部失败才报错）。程序名在每台 server 上唯一（agent 侧身份），跨 server 允许重名。
- **节点库存**（`GET /labs/{id}/nodes/{nodeId}/inventory`）：拓扑图点击 server，抽屉新增"程序"标签页，实时从该机 agent 拉取已安装包列表（agent 新增 `ListPackages` RPC，按 sha 标记只报告完整版本）与全部程序（含终端里自建的程序，界面标注"非托管"）；Controller 侧按 ServerName+Name 匹配打 `managed` 标记。
- **容器内 program 命令**：`node-cli` 新增 `program` 命令组（软链 `program`），语义与 UI Programs 页逐条对齐——`deploy <name> <pkg>[@ver] [--type oneshot] [--auto-start] [--restart-policy P] [-- args]`（按需从仓库装包、注册不启动）、`start`（oneshot 即"运行一次"）、`stop/remove/list/logs`。终端里建的程序是**节点本地**的：agent 重启/开机自启语义齐全，但 Controller 不管理、重部署不恢复（对应真实世界里绕过配置管理手写 systemd unit），在 UI 库存页可见并标注。
- **接线**：`pkg install` 的"索引解析 + 幂等安装"抽成 `resolveEntry/installEntry` 供 pkg 与 program 共用；启动脚本软链增加 `program`（golden 基线随之更新）。
- **文档**：新增 [node-agent.md](node-agent.md)——agent 的定位/状态目录/gRPC API/程序模型（systemd 语义映射）/运行逻辑（boot 恢复、监督循环、优雅退出、自愈、托管 vs 非托管），以及 `pkg`/`program` 两组容器内命令的完整参考。

### Server 监控（node-exporter 风格资源采样）

参考 node-exporter 的采集项，为 server 节点补上资源监控，走 node-agent 通道（与节点库存同路径），不经 docker exec。

- **采集语义**（共享内核容器的适配）：CPU/内存/磁盘 I/O 取自容器自身 cgroup v2 子树（`cpu.stat`/`memory.current`/`io.stat`），进程数取自容器 PID namespace（`/proc` 数字目录计数），网卡流量取自容器 netns（`/proc/net/dev`），运行时长为 PID 1 年龄；负载为共享宿主内核值（各 server 相同，UI 已标注）。cgroup 控制器不可用时 CPU/内存回退到宿主 `/proc/stat`、`/proc/meminfo`。
- **口径**：CPU 使用率按 `cpu.max` 配额归一（0–100%，无配额时用宿主核数）；内存 used 采用 working set 口径（`memory.current - inactive_file`，对齐 node-exporter 的 MemAvailable 惯例）。
- **暴露方式**（重构后）：agent 在管理网暴露标准 Prometheus text format 端点 `GET :9100/metrics`（node-exporter 惯例端口，`dcnetlab_node_*` 前缀，详见 [node-agent.md](node-agent.md)），只导出累计 counter 与瞬时 gauge——速率由抓取方差分，agent 完全无状态；gRPC 面回归纯进程管理。
- **链路**：Controller data 层 HTTP 抓取 + text format 解析（未知指标族忽略、坏行跳过）→ `GET /labs/{id}/nodes/{nodeId}/metrics`（`ProgramUsecase.NodeMetrics`，实时速率 = 本次抓取对历史采集器最新点的差分，≤15 s 区间均值）→ 拓扑图 server 抽屉"监控"标签页：资源用量进度条（CPU/内存/根文件系统，75%/90% 变色）、负载与磁盘 I/O 描述表、逐网卡收发速率表，打开时每 5 s 静默自刷新（失败保留上一采样，不弹错）。
- **测试**：采集器解析函数为纯函数表驱动测试；`Collect` 与 text format 编码器走假 `/proc` + cgroup 目录树；controller 侧解析器覆盖标签/转义/坏行/未知指标族；biz 层覆盖非 server/未部署/agent 不可达/基线差分路径。

### Server 监控历史数据与 Prometheus 出口

在实时采样之上补历史时序，速率口径从"瞬时抽样"升级为 counter 差分（Prometheus rate 语义）。

- **counter 差分**：agent 的 `/metrics` 端点导出累计计数器（CPU 秒、磁盘字节/操作数、网卡字节/包/错误/丢包）；controller 侧 `internal/metrics.Collector`（Kratos transport server，生命周期同 observer）每 15 s 并发抓取全部 deployed server，相邻 sweep 差分得区间平均速率，回绕钳零、单台失败记 gap（counter 累计性保证 gap 后差分依然正确）；首个样本只做基线不出点，基线超 1 h 弃用。
- **存储**（`internal/metrics.History`）：内存环形窗口（15 s × 2 h，存差分后 gauge）+ JSONL 按小时分片 `data/labs/<id>/metrics/<UTC 小时>.jsonl`（行内同时存 gauge 与 counter，后者供重启恢复差分基线）；单写者 O_APPEND、每 sweep flush 不 fsync（崩溃最多丢 15 s）；翻转/首次触达时删超 24 h 分片；启动回放最近 2 h 重建窗口，撕裂行跳过；lab 删除随目录消失，内存态由 Retain 清理。
- **历史 API**：`GET /labs/{id}/nodes/{nodeId}/metrics/history?start=&end=`（缺省最近 30 分钟，`step` 参数预留）；≤2 h 走内存，更早扫分片。历史不要求 deployed generation——停机的 lab 也能回看。
- **Prometheus 出口**：controller `GET /metrics`（text format 0.0.4,手写编码器不引依赖），命名贴 node-exporter（`dcnetlab_server_cpu_seconds_total{mode=…}`、`dcnetlab_server_network_receive_bytes_total{iface=…}` 等,标签 lab/server），只导出 1 分钟内的新鲜点,counter 原样透出由外部 Prometheus 自行算 rate。
- **UI**：监控标签页下方新增"历史曲线"区——时间范围切换（30m/1h/2h/24h）+ 四块 ECharts 折线（CPU 三系列、内存 used/limit、网卡按接口筛选 rx/tx、磁盘读写）；ECharts 按需引入（LineChart + Grid/Legend/Tooltip + Canvas）,采集 gap 断线不插值（`connectNulls: false`）,配色用经 CVD 校验的分类调色板前三槽位；随实时区静默轮询（历史 30 s 一刷）。监控 tab 激活时抽屉动态加宽（60%,CSS 上限 900px,其余 tab 保持 420px）,实时区与四图用 `auto-fit` 网格——窄单列/宽双列自适应,图表随 ResizeObserver 重排。
- **测试**：History 覆盖追加/窗口裁剪/跨小时翻转/过期清理/撕裂行回放/盘上查询/Retain；Collector 覆盖首点基线、双 sweep 差分、agent 失联 gap、counter 回绕钳零、基线超龄；/metrics endpoint 覆盖格式/标签/陈旧点过滤。

### 仿真外网访问（internet access）

模拟真实数据中心的互联网出口：server 的外网流量走完整仿真路径 server → leaf → spine → superspine → dcedge → external → 真实互联网，设备故障时外网访问真实中断。创建 lab 时可选开启（`CreateLabRequest.internet_access`，UI 开关），关闭即气隙 DC，行为与之前完全一致。

- **职责划分对齐真实架构**：dcedge 是 DC 运营方边界，做业务语义 NAT——对非 `10.0.0.0/8` 目的、非 eth0 出口的流量 `SNAT --to-source <loopback>`（exec 注入）。loopback 唯一且 BGP 可达，回程天然锚定到持有 conntrack 状态的那台 dcedge，双边 ECMP 下会话一致；目的仍在 10/8 的流量（未来 DCI）不做 NAT。external 代表运营商/骨干（多 DC 演进时的共享层）：部署后接入共享 docker 网络 `dcnetlab-wan`（`ConnectInternet` 步骤），默认路由指 WAN 网关，出 WAN 口再做一层机械 masquerade（Docker 宿主机只为 WAN 网段源地址 NAT，语义等价运营商 CGN），并对 dcedge 邻居 `default-originate`——默认路由经 eBGP 逐级下发到 leaf，server 侧零改动。
- **镜像**：官方 FRR 镜像无 iptables，`build/frr-edge/Dockerfile`（FROM frr + `apk add iptables`）经 `make edge-image` 构建为 `dcnetlab/frr-edge:10.2.1`，仅 dcedge/external 在开关开启时使用；apply 流水线前置 `EnsureEdgeImage` 步骤缺镜像时快速失败并提示构建命令。
- **实现位置**：模型/API 开关（`TopologySpec.InternetAccess`）；FRR 编译器 `NeighborConfig.DefaultOriginate`；containerlab 编译器 `dcedgeExec` + `EdgeImage`；runtime `internal/runtime/internet.go`（建网/接入/找 WAN 口/配路由与 masquerade，全程幂等）；biz apply 流水线 `ConnectInternet` 步骤（noop runtime 跳过）。BGP 视图（GetNodeBGP）与部署共用同一编译器，default-originate 同步呈现。
- **测试与验证**：编译器测试覆盖开关两态的产物差异；e2e 验证 traceroute 全路径穿 fabric、`apk add`/HTTPS 可用、dcedge SNAT 计数命中、pause dcedge 断外网且 unpause 自愈、未开启的 lab 保持气隙。

### Traffic 子系统（设计 §15，Iteration 4）

用户可以在两台 server 之间跑可度量的流量、设断言、看实时曲线（设计文档 Iteration 4 交付项：Traffic Scenario / HTTP·TCP·UDP Client / RPS·成功率·时延 / Traffic UI 全部达成）。路线上跳过了 Iteration 3 剩余项（就绪门控排序、GenericDaemonProvider），直接做 Traffic——trafficgen 已就绪、演示价值更高，前者留到接 Pingmesh 前再做。

- **trafficgen 增强**（`serverapps/internal/trafficgen`）：延迟统计从单条 avg/max 升级为窗口内采样（`stats.go` 加锁存 slice）每 5 s 排序算 p50/p95/p99；http/tcp/udp 三个 client 都加 `--concurrency N`（每个 worker 独立 ticker + 连接，互不影响）与 `--payload-bytes N`（http 走 POST body；tcp 在 seq 行后追加 filler 字节，服务端 `bufio.Scanner` 缓冲区同步调大到 1 MiB 避免行截断；udp 追加到数据报，受 64 KB 报文上限约束单独限流 60000）。
- **TrafficScenario 资源**（`internal/model/traffic.go` + biz/data/service 分层 + `pb` API，独立于 Program 与 Plan/Apply）：`Spec` 含源/目标 server、protocol（http/tcp/udp）、port（按协议给默认值 8080/9000）、rate（映射 client `--interval=1/rate`）、concurrency、payloadBytes、duration（0=一直跑到手动停止）、assertions（`[]{metric, comparator, threshold}`，metric ∈ rate/successRate/p50/p95/p99）；`Status` 含两个 Program 的 id/name、最新窗口指标、逐条断言实时 pass/fail。
- **生命周期**：Start 复用现有 `ProgramUsecase.CreateProgram` 在目标 server 建 `<name>-server` Program、源 server 建 `<name>-client` Program（`RestartPolicy=Always`，target IP 用现成的 `node.Spec.Address.Addr()` 查询）；client 创建失败会回滚已建的 server Program，不留孤儿；Stop 只停两个 Program 不删（重启快，对齐 `programs` 页可见但非独立管理）；Delete 级联删两个 Program。**关键坑**：`dcnetlab-trafficgen` 是按 `os.Args[1]` 分发模式的多命令二进制（`http-server`/`http-client`/…），`Program.Spec.Args` 的第一个元素必须是模式名——上线前的真实环境验证抓到这个漏传（`serverArgs`/`clientArgs` 最初只拼了 flag，没拼模式，进程无限崩溃重启），修复后补了拆包级测试断言 `Args[0]`，光靠 flag 内容的测试断言不出这类问题。
- **指标管道**（`internal/traffic`，独立于 `internal/metrics`）：`Collector` 每 5 s（贴合 trafficgen 自身打点间隔）轮询每个 Running scenario，复用现成的 `Agent.TailLogs` 拉 client 程序最近几行日志，按 JSON `msg=="stats"` 取最新一行，累计 total/failed 与上次基线做差得到窗口内 rate/successRate，直接透传 trafficgen 自带的 p50/p95/p99；首次采样只种基线不出点（避免生命周期起点的虚假瞬时值）；`duration` 到期由 Collector 反向调用 biz 层 `Stopper` 接口自动停止（`wire.Bind` 鸭子类型绑定，`internal/traffic` 不反向依赖 `biz`）。`History` 只保内存环形窗口（1 小时 @ 5 s），不落 JSONL——Scenario 是交互式短生命周期实验，重启/重部署后本就要重新 Start，不需要跨重启保留历史，区别于 server 监控。
- **Traffic UI**：新页面（列表 3 s 轮询 / 创建对话框选源+目标 server、协议、速率/并发/负载/持续时间、动态加断言 / 启停删除 / 实时指标列 + 断言 pass/fail 角标 / 图表抽屉复用 `MetricsChart.vue` 展示成功率+速率+p50/p95/p99 三块，5 s 轮询贴合采集节奏）。
- **真实环境验证**（dc1，25 容器）：Create → Start 在真实 server 上起两个 trafficgen 进程（`pid` 非零、无重启、无 lastError），日志确认 client 按 `--concurrency`/`--interval` 并发发请求；发现并修复了上面记录的模式参数漏传坑；受当前 dc1 环境的已知漂移（长期运行 + WSL2 容器重启导致的 veth 丢失，见「关键经验记录」第 3 条）影响，本轮验证到"进程正确起停 + Collector 正确采集并如实反映失败"为止（assertion 正确判定 successRate 未达标），完整穿透 fabric 的成功流量待下次 Plan/Apply 重部署后复验；测试场景创建/启动/停止/删除全流程无残留（programs 与 scenario 均清理干净）。

### 故障注入子系统（设计 §17，Iteration 6）

用户可以对拓扑里的一个节点或一条链路注入受控故障、观察 Traffic 曲线掉坑、再一键恢复（设计文档 Iteration 6 交付项：Link Down / Node Stop / Delay / Loss / Rate Limit / 故障恢复 全部达成；Interface Down 顺带一起做；Traffic 联动本轮只做真实环境验证，Capture 联动因 It. 5 尚未开工推迟）。

- **资源建模**（`internal/model/fault.go` + biz/data/service 分层 + `pb` API，独立于 Program/Traffic）：单一 `FaultScenario` 资源，`Spec.Target` 区分 `node`/`link`（link 额外带 `side: a/b/both`），`Spec.Type` 覆盖 `node-stop`/`node-restart`/`link-down`/`interface-down`/`impairment`。**两处对设计文档字面的偏差**（讨论阶段与用户对齐过，见下文「已知偏差」）：Delay/Jitter/Loss/Rate Limit 合并成一个 `impairment` 类型而非四个独立类型（对齐 `tc netem` 本身"一张网卡一个 qdisc"的物理事实，四个独立类型会需要内部合并/回退逻辑，UI 层的独立并不能消掉底层的合并复杂度）；同一 target 同一时刻只允许一个生效故障，Recover 直接恢复固定基线（接口 up / 无 qdisc / 容器 running），不做故障前快照结构——故障前状态在当前平台里永远是部署时写入的标准基线，等价于快照但不用真存一份。
- **执行方式**：`node-stop`/`node-restart` 直接复用 `PowerUsecase.StopNode`/`StartNode`（pause/unpause 语义，理由见下）；`link-down`/`interface-down`/`impairment` 走 `runtime.Driver` 新增的 `SetInterfaceState`/`ApplyImpairment`/`ClearImpairment` 三个方法（`internal/runtime/fault.go`），跟 `ConnectInternet` 一样直接 `docker exec` 跑 `ip link set <iface> up/down` 与 `tc qdisc replace/del dev <iface> root netem ...`，不经过 server agent；`tc qdisc replace`（而非 `add`）与 `ip link set`（本身幂等）让 Apply 不需要读现有状态。`node-restart` 是瞬时事件（Stop 紧接 Start），Apply 后 `Applied` 直接落回 `false`，Recover 对它直接拒绝（"已自动恢复"）。
- **Node Restart 选了 pause/unpause 而非真实 docker restart**：讨论阶段的关键取舍——真实容器重启在 WSL2/OrbStack 环境下会撞上「关键经验记录」第 3 条已知坑（veth 全部丢失，需要重新 Plan/Apply 才能恢复），会把"点 Recover 应该恢复正常"变成"点了之后链路还是断的"，与故障注入"故障可控、恢复优雅"的定位冲突；等这个环境限制解决或有明确场景要演示 agent 冷启动自愈，再升级成真实重启。
- **一致性与回滚**：Apply 前用 `checkTargetFree` 拒绝对同一 target 的第二次 Apply（提示先 Recover）；`link-down`/`impairment` 需要操作两个端点时（如 `link-down` 两端一起 down），后一个端点失败会把前一个已成功的端点回滚，不留半生效状态；Delete 若仍生效会先自动 Recover 再删，不留下"资源没了但链路还断着"的孤儿态。
- **拓扑图联动**：`TopologyCanvas.vue` 的 edge "dead" 判定从"只看端点节点是否整体停机"扩展为"再看这条 link 两端接口各自的观测 up 状态"（`Node.Status.Interfaces`，Observer 每 6 s `ip -br link` 采集，此前只在节点抽屉的接口表里展示、没人拿来画图）——`link-down`/`interface-down` 故障生效后拓扑图会自动把对应链路变灰，不需要拓扑组件知道 `FaultScenario` 这个资源存在，纯粹是把已经在采的真实数据画出来；`impairment` 因为 qdisc 不改变 operstate，不会体现在拓扑图颜色上（预期内，效果要看 Traffic 曲线）。
- **UI**：独立「故障」页面（结构比照 Traffic 页面：列表 3 s 轮询 / 创建对话框先选 target 类型再联动出节点或链路下拉、故障类型、side、impairment 参数 / 注入·恢复切换按钮 / 生效中删除会先自动恢复）；另外在拓扑图的节点抽屉加「故障」标签页（快捷注入 node-stop/node-restart，需二次确认）、链路抽屉加"注入故障"小节（link-down / 分端 interface-down / 内联 impairment 小表单，均为一键创建即 Apply，target 已知不用再选一次）——两处都会列出已作用于当前节点/链路的故障并可直接 Apply/Recover/删除。
- **测试**：`internal/runtime/fault_test.go` 表驱动覆盖 `netemArgs` 的参数组合（delay+jitter/loss/rate 任意组合、jitter 无 delay 时被丢弃、全空时不生成参数）；`internal/biz/fault_test.go` 用假 driver 记录 exec 调用，覆盖校验规则（type/side/impairment 合法性）、target 名称解析、link-down 双端下发与恢复、interface-down 单端、impairment 参数透传、同 target 二次 Apply 拒绝、双端故障后端失败时前端回滚、node-stop/node-restart 生命周期、Delete 前自动 Recover。
- **真实环境验证**（my-dc，25 容器，standard profile）：TDD 方式——先写断言脚本（Python 直打 REST API + `docker exec` 检查内核真实状态）再跑，覆盖全部 5 种故障类型：node-stop/restart 的容器真实 pause/unpause、link-down 两端 `ip link set down` 真实生效并被 Observer 6 s 扫描周期采到、interface-down 单端只下发到目标端、impairment 的 `tc qdisc` 真实带上 `delay 100ms 10ms loss 1% rate 5Mbit`、同一 target 二次 Apply 真实被拒绝、Delete 前自动 Recover、node-restart 拒绝二次 Recover——32 项断言全过。前端用 Playwright（Firefox，headless Chromium 因缺系统库且无 sudo 装不上）跑真实浏览器：链路故障生效后拓扑图确实自动变灰（WebSocket 帧内容与像素级截图交叉验证，flags/state 需要区分 admin 与 oper 两层）、节点抽屉「故障」标签页与链路抽屉"注入故障"小节均按设计渲染、全程 0 个浏览器控制台错误。过程中发现并修复/确认了三件事（均非本轮代码缺陷）：
  1. **验证前置条件**：my-dc 因宿主长期运行撞上「关键经验记录」第 3 条已知坑（veth 全部丢失），验证前先跑一次 Plan/Apply 重部署恢复,并同步刷新了本节此前记录的所有节点/链路 ID 示例。
  2. **interface-down 单端的物理边界**：只对目标端跑 `ip link set down`，veth 对端不会被我们的代码触碰，但内核会让对端天然失去 carrier（`NO-CARRIER`，`state DOWN`，自身 `IFF_UP` 管理位不变）——这是 veth pair 的物理事实（等价于拔掉一根网线的一端两头都断），"side" 控制的是"命令在哪端执行"，不是"数据还通不通"（两端物理上共享同一根线,通不通不取决于哪端被标记 down）。
  3. **拓扑图变灰的视觉强度**：现有 `edge.dead` 样式（`line-style: dotted`、`#c0c4cc`、`opacity: 0.5`）是照抄"节点掉线"沿用的既有视觉语言，在整页截图里非常不显眼，需要裁剪放大才能确认生效——功能正确，但作为故障演示的核心视觉反馈,视觉强度是否需要加强（比如更饱和的颜色）值得后续单独讨论,这轮不擅自改动既有配色决定。

### 网络漂移检测与一键修复

上面第 1 条发现的"veth 丢失"坑本身也顺手解决了——用户追问"针对这个坑有什么方案"后，讨论出的方案（Observer 检测 + 友好提示 + 用户确认修复，而非纯手动或全自动）已落地并真实验证。

- **核心洞察（决定了整个方案的可行性）**：`InterfaceStatus` 此前把"接口在内核里完全不存在"（veth 丢失）和"接口存在但状态 down"（管理性关闭）统一折叠成 `up: false`（`internal/observer/collect.go` 旧注释原话："an interface missing from the kernel ... counts as down"）。而故障注入的 `link-down`/`interface-down` 只会 `ip link set down`，永远不会让接口从内核里消失——**"missing"这个信号在结构上就不可能由故障注入产生**，不需要反查 FaultScenario 记录做交叉比对，区分故障和漂移是免费的。`internal/model/node.go` 的 `InterfaceStatus` 加了一个 `Missing bool` 字段，`interfaceStatuses()` 按 `states[name]` 是否命中来置位。
- **修复机制验证**（关键决策依据）：动手前先用真实环境验证了一个假设——丢失的 veth 能不能仅凭 `containerlab deploy --reconfigure`（不做任何 exec 配置重新下发）就自愈？结论是可以：手动删掉一个节点的 veth 后单独跑 `containerlab deploy --reconfigure`，8 秒内 BGP 和接口状态全部自动恢复（FRR/zebra 本身监听 netlink，接口一重新出现就自己重新建邻居，配置文件从未变过不需要重新下发）。这让 `RepairLab`（`internal/biz/plan.go`）非常轻量：只重放 `DeployTopology`（`Driver.Deploy` 指向**当前** generation 目录，不生成新 generation）+ `ConnectInternet` + `ValidateControlPlane`/`ValidateDataPlane` 四步，跳过 `RestorePrograms`（容器本身没被摧毁，程序不受影响）——不走完整 Plan/Apply，generation 和所有资源 ID 保持不变，比此前"重新 Plan/Apply"的临时应对方式快得多、副作用小得多。
- **前端提示**（`TopologyPage.vue`）：只对 `phase === 'Running'` 的节点检查 `status.interfaces` 里是否有 `missing`（跳过 Paused/Applying 节点，避免故意暂停的节点或部署中节点被误判），命中则在拓扑图顶部显示醒目的 warning 提示条，文案明确写"这不是你注入的故障"、列出受影响节点、附一键修复按钮（复用 `store.powerLab` 的操作轮询模式）。提示条标题旁加了一个 `QuestionFilled` 图标，hover 展示详细技术原因（容器仍在运行、只是 veth 从内核消失，常见于宿主 Docker 重启未开 live-restore；并重申故障注入只会把接口关闭不会让接口消失），把"是什么"和"为什么"分层，默认只看一行摘要即可。
- **真实环境验证**：故意删除一条链路两端的 veth 模拟漂移，确认 API 正确报出 `missing: true`、前端 Playwright 真实浏览器截图确认提示条文案和受影响节点列表正确、点击修复后提示条消失、节点 BGP/接口计数完全恢复、lab generation 和 ID 全程不变、且用户当时正在生效中的另一条 `impairment` 故障场景全程未受任何影响（修复操作与故障场景互不干扰，验证了两者的隔离性）。
- **veth 对端级联丢失，非独有于整机重启**：用户用真实拓扑手动 `docker stop` 了一台 superspine，观察到 6 个健康节点（该 superspine 在拓扑图里的全部直连邻居：2 个 dcedge + 4 个 pod spine，度数正好是 6）同时被判定为漂移，追问"ECMP 应该还通，为什么会有 6 个节点受影响"。根因确认：veth 是内核里的成对对象，删掉一端（容器网络命名空间被销毁）内核会自动连带删掉对端，不管对端在哪个容器里——所以单个节点被手动 `docker stop`/`docker restart`（绕开平台，直接操作 Docker）会像多米诺骨牌一样，把它的每一个直连邻居也各弄丢一根接口，被波及节点数 = 该节点在拓扑图里的度数，与整机重启是否发生无关。同时用真实 BGP 会话状态和跨 fabric ping 验证了 ECMP 判断是对的——6 个邻居里没有一个真正断网，只是各丢了一条冗余链路中的一条。据此把 `driftBanner` 文案从"网络连接意外丢失"改成"网络接口意外消失…若拓扑存在冗余路径，受影响节点当前仍可能保持连通"，避免在冗余拓扑下把"丢了一条链路"渲染成"整个节点断网"；中间一版文案曾写"从内核消失"，用户指出拓扑页是仿真视角、"内核"是容器实现细节，不该出现在头条摘要里（呼应「仿真视图与管理视角分离」的既有原则），故删掉这个措辞，把实现层面的解释完全留给 hover 提示条里那段技术细节；也用这个真实损坏状态（被波及的 6 个健康节点 + 1 个 Exited 的 superspine）验证了一遍 `RepairLab`，确认它不仅补线，连已经 Exited 的容器本身也会被 `containerlab deploy --reconfigure` 一并拉起。

### 代码规范与工程化

- [golang-style.md](golang-style.md) 为 Go 代码基线；golangci-lint（`.golangci.yml`，版本固定）+ 自研 `scripts/check-style.py`（空行语义）挂在 `make lint`，零告警纳入提交门禁。
- `scripts/dcnetlab up|down|status|logs`（`make up`/`make down`）一键管理 Controller + 前端；`up --dev`（`make dev`）附带 Vite 热更新——controller 以 `--web-dev-proxy` 把非 API 请求（含 HMR WebSocket）反代到本机 Vite，浏览器始终只访问 controller 端口，不再需要 5173 直连（OrbStack 场景下 Vite 也无需再绑全部接口）。

## 关键经验记录

1. **Kratos 默认 1s 请求超时**会杀死挂在 `r.Context()` 上的长会话（docker exec 终端），WebSocket handler 须用 `context.WithoutCancel`，生命周期显式管理。
2. **Element Plus 抽屉**（即使 `:modal="false"`）有全屏包裹层拦截指针事件，需 CSS `pointer-events: none`（仅面板可交互）才能实现非模态。
3. **WSL2/Docker daemon 重启后**容器被自动重启，containerlab 建的 veth 对全部丢失（只剩 eth0），表面 Running 实际断网——需到代次目录 `containerlab deploy --reconfigure` 恢复。**已解决**：Observer 现在会区分接口"完全消失"与"存在但 down"（`InterfaceStatus.Missing`，见「网络漂移检测与一键修复」），拓扑页检测到后弹出提示条，一键调用新的 `RepairLab`（只重放 `containerlab deploy --reconfigure` 等价步骤，不走完整 Plan/Apply，generation 和资源 ID 不变）即可恢复，不用再手动登录宿主敲命令或整个重新 Plan/Apply。**OrbStack 虚机重启是同场景的加重版**：docker 先于 `/Users` 共享就绪拉起容器，server 容器的单文件 bind mount（node-agent 二进制）挂成空目录，Observer 的 agent 自愈（重放启动脚本）因此永远失败（agent.log 反复 `nohup: can't execute`），程序操作报 `connection refused`——挂载丢失自愈救不了，唯一恢复路径是 UI 上重新 Plan/Apply（`RestorePrograms` 会把包与程序全部装回并按期望状态启动）。空挂载根因已由 `orb-setup` 安装的 docker drop-in 堵住（等 `/Users` 挂载就绪再启 docker）；这个组合场景里 agent 二进制丢失才是需要完整重新 Plan/Apply 的部分（`RestorePrograms` 重装包），单纯的 veth 丢失现在可以用上面的 `RepairLab` 更轻量地恢复。
4. **WSL2 内核限制**：macvlan 不支持 `addrgenmode random`（仅 IPv6 相关，已规避）；VRRP macvlan 需显式 `up` 且 VIP 用 /32，否则 vrrpd 不发通告、双 Master。
5. **vtysh JSON 输出**：缺 `/etc/frr/vtysh.conf` 时警告会混入 stdout，产物绑定 vtysh.conf + 解析时跳到首个 `{` 双保险。
6. **wire v0.7.0** 自带的 x/tools 解析不了新版本 Go，`init-tools.sh` 用临时模块升级 x/tools 后构建。
7. **server 默认路由 exec 竞态**：`ip route replace default via <gw>` 在 zebra 尚未给 bond0 配地址时因 nexthop 不可达而失败（containerlab exec 与 FRR 启动并发），管理网默认路由反客为主、流量静默逃逸——加 `dev bond0 onlink` 让路由安装不依赖 nexthop 预先可达。
8. **`docker network connect` 接口命名冲突**：Docker 按自身 endpoint 计数给新接口起名 `eth<n>`，不知道 containerlab 已把 veth 塞进命名空间占了 eth1+，撞名报 `file exists`；但失败会推进计数器，有界重试即可越过占用序号（Docker 28 的 `com.docker.network.endpoint.ifname` 选项可指定接口名，27 忽略之）。接口名不可假设，用 endpoint IP 反查。
9. **多命令二进制的 `Program.Spec.Args` 必须自带模式名**：`dcnetlab-trafficgen` 按 `os.Args[1]` 分发子命令（`http-server`/`http-client`/…），只拼 flag（如 `--listen`/`--target`）会让进程立刻报 `unknown mode "--xxx"` 退出、RestartPolicy=Always 下无限崩溃重启——单测只断言了 flag 内容、没断言 `Args[0]`，这类"参数拼装漏了必需的第一位"问题必须在真实环境跑一次真实进程才会暴露，光靠 mock agent 的单测测不出来。

## 与设计文档的已知偏差

1. **Profile 定义在代码中**（`internal/topology/defaults.go`），`profiles/*.yaml` 暂未落盘，待支持自定义 Profile 时再引入，避免双份定义。
2. **管理网 IP 未在模型中分配**：containerlab 自管管理网络，管理 IP 暂未回填（Observer 已就绪，后续可补采）。
3. **Observer 会纠正 phase**：突破设计"Observer 只写 Observed 状态"的原则，漂移时自动改写 node/lab phase（有意为之，换取免人工干预的状态收敛）。
4. **采集周期**：设计 §13 的 2/3/5s 分表合并为 2s（容器状态）+ 6s（BGP/路由/接口）两档。
5. **Operation 进度仍为 HTTP 轮询**：WebSocket 目前只覆盖拓扑观测，Operation 推送后续可迁移。
6. **Daemon 未建独立资源**（设计 §9.7 的 `DaemonSpec` 内嵌 `ProgramSpec`）：存活 / 就绪检查、启动顺序等 Daemon 语义作为字段增量落在 `ProgramSpec` 上，"Daemon"即一种带健康检查的 simple Program——API、存储、agent 协议、UI 全部复用，迁移成本为零（有意为之，见"下一步计划"的建模决策）。
7. **故障类型合并**：设计 §17 字面列了 Delay/Jitter/Loss/Rate Limit 四个独立故障类型，实现合并成一个 `impairment` 类型（字段可任意组合）——对齐 `tc netem` 本身"一张网卡一个 qdisc"的物理事实，四个独立类型并不能避免底层合并，只会把合并/回退逻辑转嫁给实现（有意为之，讨论阶段与用户对齐过，详见「故障注入子系统」）。
8. **故障恢复不做快照结构**：设计 §17 要求"故障恢复必须基于故障前快照"，实现里同一 target 同一时刻只允许一个生效故障，Recover 恢复固定基线（接口 up / 无 qdisc / 容器 running）——当前平台里故障前状态永远是这个标准基线，二者事实等价，只是不存一份快照数据（有意为之，若未来要支持同一 target 叠加多个故障，需要重新引入真快照链）。

## 下一步计划（按优先级）

### 迭代路线与排序

原计划顺序为 It. 3 → It. 4 → It. 5 → It. 6；实际执行时与用户对齐后调整为 **It. 4 Traffic（已完成）→ It. 6 故障（已完成，含真实环境验证）→ It. 5 抓包 → It. 3 剩余 + It. 8 Pingmesh → It. 7 扩容 / Rollback → It. 9/10 虚拟网络 / EIP**，理由：

- **Traffic（It. 4）已提前于 It. 3 剩余项完成**：trafficgen 已就绪、演示价值高于就绪门控排序 / `GenericDaemonProvider`（两者暂无真实消费者，留到接 Pingmesh 前再做，避免为无消费者的抽象提前设计）。
- **故障实验（It. 6）紧跟 Traffic 完成**：Link Down / Node Stop / Delay / Loss / Rate Limit / 故障恢复均已交付（详见「故障注入子系统」），"注入故障 → Traffic 曲线掉坑 → 恢复"这条平台最核心的演示闭环已经打通，Traffic 的指标管道被直接复用。抓包（It. 5，AF_PACKET）为独立能力，后置不损失价值，"Traffic 和 Capture 联动"里 Capture 那半句留到 It. 5 完成后补验证。快照结构按讨论阶段的结论简化为固定基线恢复（见「已知偏差」第 8 条），未引入独立的 FaultSession/快照资源。
- **Pingmesh（It. 8）提前到扩容之前**：它是 Daemon Framework 的首个真实消费者，尽早验证 DaemonProvider 接口设计是否成立；扩容 / Rollback（Generation 快照已就绪）不阻塞任何人，何时做都行。

### Iteration 3 剩余项拆解

Auto Start 与 Restart Policy 已在"Program 的 systemd 化"一轮落地（`autoStart`、`RestartPolicy` 含指数退避、simple/oneshot 类型、agent 崩溃自愈），退出标准"Server 重启后 Required Daemon 自动恢复"的机制链路已全通，剩余（留到接 Pingmesh 前再做）：

1. ~~**健康检查**（设计 §10.7）~~ ✅ 已落地：Process/TCP/HTTP 存活 + 就绪探测（详见上文「Program 存活检查」「Program 就绪检查与启动顺序」）。
2. ~~**启动顺序**（§10.8）~~ 🚧 部分落地：`StartupOrder` 整数排序在 agent 开机与 `RestorePrograms` 两条路径生效（不做 `dependsOn` DAG）。**剩就绪门控排序**——"等前序 order 组 ready 再启后序"，就绪状态已就位作为门控信号；涉及开机时延与门控归属（agent 侧 vs controller 侧）取舍，待定后补。
3. **配置渲染 + `GenericDaemonProvider`**（§10.9）：Pingmesh 的地基；RenderedFile 下发建议走现有包安装通道的旁路（agent 增加写配置文件能力，配置与包一样带校验和、幂等），不开新协议。
4. **Daemon UI**（§14.2）：倾向在 Programs 页面增加类型维度与健康状态列（健康 / 就绪角标、启动顺序已加），不新建独立页面。

**建模决策**：`DaemonSpec` 内嵌 `ProgramSpec`（§9.7），但 `type`/`autoStart` 已进 `ProgramSpec`。建议不引入独立 Daemon 资源，继续在 Program 上增量加字段（healthCheck、startupOrder、required、providerType），"Daemon"退化为一种带健康检查的 simple Program——API、存储、agent 协议、UI 全部复用，迁移成本为零；代价是与设计文档资源模型有出入，落地时需在设计文档或"与设计文档的已知偏差"一节正式确认。

### 迭代外技术债

1. **Operation 进度仍为 HTTP 轮询**：Traffic 页面这轮同样保持轮询（列表 3 s、图表 5 s），未迁入现有 WebSocket 通道；故障实验若需要更低延迟的曲线联动，是把 Operation/Traffic 指标统一迁到 WebSocket 的合适时机。
2. **Package 格式扩展**：deb（需 Debian 系 server 镜像）与 OCI Image 作为 `format` 扩展位；随 It. 7 扩缩容触碰镜像与装机链路时引入"集成进镜像"的预装通道（第一个搬进镜像的是 agent）。
3. **多 DC 互联**：dcedge 每 DC 独享、共享 external 骨干层；DC 间流量（目的 10/8）已在边界 NAT 规则中预留不做转换，对应真实 DCI 语义。
4. **控制面 / 数据面分离（宿主侧 runtime agent）**：Controller 与 docker 宿主现有四处耦合——子进程直调 `containerlab`/`docker` CLI、编译产物以宿主绝对路径作 bind mount 源、经管理网直拨容器内 agent 的 gRPC、agent 按管理网网关地址回拉软件包（假设 Controller 就在网关侧）。`runtime.Driver` 接口已是天然切分缝隙，做多宿主 / 多 DC（上条）时引入宿主侧 runtime agent（产物传输 + exec 流代理 + 双向网络打通）即可分离；单机场景不提前拆。详见 [macos.md](macos.md)「为什么必须进虚机」。

## 环境备注

- 开发机：WSL2，Go 1.23+、Node 24、Docker 27、containerlab 0.77（SUID + `clab_admins`，免 sudo）；内核支持 bonding/8021q/macvlan/bridge vlan_filtering。
- macOS：经 OrbStack 虚机运行真实部署（见上文「macOS 适配」），`make orb-setup` 一次装机，`make up` 自动转发；虚机内 Docker 28 / Ubuntu。
- 运行入口：`make up` / `scripts/dcnetlab up [--dev]`（推荐），或 `make run`（仅后端）+ `make web-dev`（前端开发模式）。
