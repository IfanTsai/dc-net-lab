# DCNetLab 项目进度记录

> 本文档跟踪实际开发进度，按设计文档（[design.md](design.md)）第 26 节的迭代计划组织。每次迭代完成或有重要进展时更新。

## 总览

| 迭代 | 内容 | 状态 |
| --- | --- | --- |
| Iteration 0 | 运行时验证：Controller 骨架、Vue 骨架、两节点拓扑 | ✅ 已完成 |
| Iteration 1 | 物理网络闭环：Profile、IPAM、ASN、FRR 编译、Plan/Apply、Topology UI | ✅ 已完成，含真实部署验证 |
| Iteration 2 | Server Program:Compute Server Container、Server Agent、trafficgen | ✅ 已完成，含 Package 制品部署 |
| Iteration 3 | Daemon Framework | 🚧 进行中（Auto Start / Restart Policy / 存活 + 就绪检查 / 启动顺序已落地；剩就绪门控排序、配置渲染 + Provider、Daemon UI） |
| Iteration 4 | Traffic | ⬜ 未开始 |
| Iteration 5 | 原生抓包（AF_PACKET） | ⬜ 未开始 |
| Iteration 6 | 故障实验 | ⬜ 未开始 |
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
- 单测覆盖漂移纠正、稳态零写、未部署跳过、订阅广播与解析函数。

### Web UI(Vue 3 + TypeScript + Pinia + Element Plus + Cytoscape.js)

- **三页面**：Labs（创建/Plan 预览/Apply/删除）、Topology、Operations（步骤进度，2s 轮询）；vue-i18n 国际化（中/英，默认跟随系统，可切换）。
- **拓扑画布**：分层 Clos 布局 + Pod/机柜 compound 分组框；按角色语义化设备图标（内联 SVG），右上角实时状态角标（绿=运行且 BGP 全建立 / 橙=BGP 未收敛 / 灰=暂停 / 红=异常）；所有链路统一细的近黑灰（`#303133`）静态实线，选中设备蓝色边框并联动高亮直连链路，停止设备及其链路置灰；观测推送走**增量更新**（结构未变时原地刷新图标/类，不重置用户缩放/拖拽）；节点抽屉分「模拟视角 / 运行时」两个标签页——模拟视角展示配置 + 实时观测（运行状态、BGP 会话 x/y、路由数、接口 up x/y、VRRP 网关角色、最后观测时间）、接口表（拓扑链路 + vlanif/bond0，逐接口 up/down，本端/对端地址）与 BGP 配置表（`GET /api/v1/labs/{labId}/nodes/{nodeId}/bgp`，`TopologyUsecase.GetNodeBGP` 直接复用 `frr.BuildRouterConfigs`，展示邻居/远端 AS/对端与 leaf 的 SERVERS 动态监听组，与下发的 frr.conf 永不漂移），路由三层按真实运维逻辑分标签页按需拉取——**BGP**（`/nodes/{id}/bgp-table`，`show ip bgp json` 的 Loc-RIB 全部候选路径，best/multi/iBGP 标记 + AS-Path + 下一跳设备名，best path 选择前的视角）、**RIB**（`/nodes/{id}/routes`，zebra 路由表，前缀/协议/AD/Metric/ECMP 下一跳）、**FIB**（`/nodes/{id}/fib`，内核 `ip -j route show table all`，对齐真实交换机的两层：main 表 LPM 转发条目 + local 表本机条目（标 `local`，等价 ASIC host/punt 表），broadcast/multicast/127/8/IPv6 等内核噪声过滤），三层条目数呈漏斗递减，可直观对照 best path 选择与 RIB→FIB 下装；运行时标签页按需拉取底层容器状态与全部内核接口（含 br0、VRRP macvlan、eth0 等实现细节）；抽屉非模态（开着也能继续操作画布，点画布空白自动收回）。
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

### 代码规范与工程化

- [golang-style.md](golang-style.md) 为 Go 代码基线；golangci-lint（`.golangci.yml`，版本固定）+ 自研 `scripts/check-style.py`（空行语义）挂在 `make lint`，零告警纳入提交门禁。
- `scripts/dcnetlab up|down|status|logs`（`make up`/`make down`）一键管理 Controller + 前端；`up --dev` 附带 Vite 热更新。

## 关键经验记录

1. **Kratos 默认 1s 请求超时**会杀死挂在 `r.Context()` 上的长会话（docker exec 终端），WebSocket handler 须用 `context.WithoutCancel`，生命周期显式管理。
2. **Element Plus 抽屉**（即使 `:modal="false"`）有全屏包裹层拦截指针事件，需 CSS `pointer-events: none`（仅面板可交互）才能实现非模态。
3. **WSL2/Docker daemon 重启后**容器被自动重启，containerlab 建的 veth 对全部丢失（只剩 eth0），表面 Running 实际断网——需到代次目录 `containerlab deploy --reconfigure` 恢复（未来可做成"修复/重部署"操作；Observer 能暴露此类假 Running）。
4. **WSL2 内核限制**：macvlan 不支持 `addrgenmode random`（仅 IPv6 相关，已规避）；VRRP macvlan 需显式 `up` 且 VIP 用 /32，否则 vrrpd 不发通告、双 Master。
5. **vtysh JSON 输出**：缺 `/etc/frr/vtysh.conf` 时警告会混入 stdout，产物绑定 vtysh.conf + 解析时跳到首个 `{` 双保险。
6. **wire v0.7.0** 自带的 x/tools 解析不了新版本 Go，`init-tools.sh` 用临时模块升级 x/tools 后构建。
7. **server 默认路由 exec 竞态**：`ip route replace default via <gw>` 在 zebra 尚未给 bond0 配地址时因 nexthop 不可达而失败（containerlab exec 与 FRR 启动并发），管理网默认路由反客为主、流量静默逃逸——加 `dev bond0 onlink` 让路由安装不依赖 nexthop 预先可达。
8. **`docker network connect` 接口命名冲突**：Docker 按自身 endpoint 计数给新接口起名 `eth<n>`，不知道 containerlab 已把 veth 塞进命名空间占了 eth1+，撞名报 `file exists`；但失败会推进计数器，有界重试即可越过占用序号（Docker 28 的 `com.docker.network.endpoint.ifname` 选项可指定接口名，27 忽略之）。接口名不可假设，用 endpoint IP 反查。

## 与设计文档的已知偏差

1. **Profile 定义在代码中**（`internal/topology/defaults.go`），`profiles/*.yaml` 暂未落盘，待支持自定义 Profile 时再引入，避免双份定义。
2. **管理网 IP 未在模型中分配**：containerlab 自管管理网络，管理 IP 暂未回填（Observer 已就绪，后续可补采）。
3. **Observer 会纠正 phase**：突破设计"Observer 只写 Observed 状态"的原则，漂移时自动改写 node/lab phase（有意为之，换取免人工干预的状态收敛）。
4. **采集周期**：设计 §13 的 2/3/5s 分表合并为 2s（容器状态）+ 6s（BGP/路由/接口）两档。
5. **Operation 进度仍为 HTTP 轮询**：WebSocket 目前只覆盖拓扑观测，Operation 推送后续可迁移。
6. **Daemon 未建独立资源**（设计 §9.7 的 `DaemonSpec` 内嵌 `ProgramSpec`）：存活 / 就绪检查、启动顺序等 Daemon 语义作为字段增量落在 `ProgramSpec` 上，"Daemon"即一种带健康检查的 simple Program——API、存储、agent 协议、UI 全部复用，迁移成本为零（有意为之，见"下一步计划"的建模决策）。

## 下一步计划（按优先级）

### 迭代路线与排序

原计划顺序为 It. 3 → It. 4 → It. 5 → It. 6，建议调整为 **It. 3 剩余 → It. 4 Traffic → It. 6 故障 → It. 5 抓包 → It. 8 Pingmesh → It. 7 扩容 / Rollback → It. 9/10 虚拟网络 / EIP**，理由：

- **Traffic（It. 4）依赖度低、演示价值高**：trafficgen 六模式与 JSON 打点已就绪，Traffic Scenario 本质是"一对 Program 的编排 + 指标汇聚"，而指标管道（controller 拉取 + 内存窗口 + JSONL 分片 + `/metrics` 出口）已在 Server 监控落成，可直接复用 `internal/metrics` 的 History/Collector 模式；主要新工作是 Scenario 资源模型、断言（Assertions）与 Traffic UI（P50/P95/P99 曲线）。
- **故障实验（It. 6）紧跟 Traffic**：Node Stop 已有（pause/unpause），Link Down/Delay/Loss/Rate Limit 均为 netem/netlink 操作，"注入故障 → Traffic 曲线掉坑 → 恢复"是平台最核心的演示闭环。抓包（It. 5，AF_PACKET）为独立能力，后置不损失价值。设计 §17 要求"故障恢复基于故障前快照"，需引入 FaultSession 资源持有快照，是该迭代的建模重点。
- **Pingmesh（It. 8）提前到扩容之前**：它是 Daemon Framework 的首个真实消费者，尽早验证 DaemonProvider 接口设计是否成立；扩容 / Rollback（Generation 快照已就绪）不阻塞任何人，何时做都行。

### Iteration 3 剩余项拆解

Auto Start 与 Restart Policy 已在"Program 的 systemd 化"一轮落地（`autoStart`、`RestartPolicy` 含指数退避、simple/oneshot 类型、agent 崩溃自愈），退出标准"Server 重启后 Required Daemon 自动恢复"的机制链路已全通，剩余：

1. ~~**健康检查**（设计 §10.7）~~ ✅ 已落地：Process/TCP/HTTP 存活 + 就绪探测（详见上文「Program 存活检查」「Program 就绪检查与启动顺序」）。
2. ~~**启动顺序**（§10.8）~~ 🚧 部分落地：`StartupOrder` 整数排序在 agent 开机与 `RestorePrograms` 两条路径生效（不做 `dependsOn` DAG）。**剩就绪门控排序**——"等前序 order 组 ready 再启后序"，就绪状态已就位作为门控信号；涉及开机时延与门控归属（agent 侧 vs controller 侧）取舍，待定后补。
3. **配置渲染 + `GenericDaemonProvider`**（§10.9）：Pingmesh 的地基；RenderedFile 下发建议走现有包安装通道的旁路（agent 增加写配置文件能力，配置与包一样带校验和、幂等），不开新协议。
4. **Daemon UI**（§14.2）：倾向在 Programs 页面增加类型维度与健康状态列（健康 / 就绪角标、启动顺序已加），不新建独立页面。

**建模决策**：`DaemonSpec` 内嵌 `ProgramSpec`（§9.7），但 `type`/`autoStart` 已进 `ProgramSpec`。建议不引入独立 Daemon 资源，继续在 Program 上增量加字段（healthCheck、startupOrder、required、providerType），"Daemon"退化为一种带健康检查的 simple Program——API、存储、agent 协议、UI 全部复用，迁移成本为零；代价是与设计文档资源模型有出入，落地时需在设计文档或"与设计文档的已知偏差"一节正式确认。

### 迭代外技术债

1. **Operation 进度仍为 HTTP 轮询**：Traffic UI 与故障实验会加重实时推送需求，It. 4 做 Traffic UI 时是把 Operation/Traffic 指标统一迁到现有 WebSocket 通道的合适时机。
2. **Package 格式扩展**：deb（需 Debian 系 server 镜像）与 OCI Image 作为 `format` 扩展位；随 It. 7 扩缩容触碰镜像与装机链路时引入"集成进镜像"的预装通道（第一个搬进镜像的是 agent）。
3. **多 DC 互联**：dcedge 每 DC 独享、共享 external 骨干层；DC 间流量（目的 10/8）已在边界 NAT 规则中预留不做转换，对应真实 DCI 语义。

## 环境备注

- 开发机：WSL2，Go 1.23+、Node 24、Docker 27、containerlab 0.77（SUID + `clab_admins`，免 sudo）；内核支持 bonding/8021q/macvlan/bridge vlan_filtering。
- 运行入口：`make up` / `scripts/dcnetlab up [--dev]`（推荐），或 `make run`（仅后端）+ `make web-dev`（前端开发模式）。
