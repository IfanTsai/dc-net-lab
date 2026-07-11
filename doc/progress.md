# DCNetLab 项目进度记录

> 本文档跟踪实际开发进度，按设计文档（[design.md](design.md)）第 26 节的迭代计划组织。每次迭代完成或有重要进展时更新。

## 总览

| 迭代 | 内容 | 状态 |
| --- | --- | --- |
| Iteration 0 | 运行时验证：Controller 骨架、Vue 骨架、两节点拓扑 | ✅ 已完成 |
| Iteration 1 | 物理网络闭环：Profile、IPAM、ASN、FRR 编译、Plan/Apply、Topology UI | ✅ 已完成，含真实部署验证 |
| Iteration 2 | Server Program:Compute Server Container、Server Agent、lab-app | ⬜ 未开始 |
| Iteration 3 | Daemon Framework | ⬜ 未开始 |
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

- **两档采集**（`internal/observer`，Kratos transport.Server 生命周期）：2s 快速采集（一条 `docker ps -a` 批量拿全部容器状态）；6s 深度采集（并发信号量 8，对每个 running 容器单次 exec 组合脚本一次拿到接口 up/total、BGP established/configured、路由总数）。设计 §13 的 2/3/5s 分表合并为两档，减少 exec 压力；noop 运行时整体跳过。
- **漂移自动纠正**（有意突破"Observer 只写 Observed"原则）：容器实际状态映射 node phase（running→Running / paused→Stopped / 其它→Failed），lab phase 由设备推导（全开 Running / 全停 Stopped / 混合 Degraded），Planning 等过渡态不碰；手动 `docker pause`、宿主机重启等漂移在一个采集周期内自动收敛。
- **WebSocket 推送**：`GET /ws/v1/labs/{labId}/topology`，连接即发最新快照、每次采集全量推送；慢消费者跳帧不阻塞。`lastObserved` 语义为"数据最后变化时间"——仅观测值变化时落库，稳态零写放大。
- 单测覆盖漂移纠正、稳态零写、未部署跳过、订阅广播与解析函数。

### Web UI(Vue 3 + TypeScript + Pinia + Element Plus + Cytoscape.js)

- **三页面**：Labs（创建/Plan 预览/Apply/删除）、Topology、Operations（步骤进度，2s 轮询）；vue-i18n 国际化（中/英，默认跟随系统，可切换）。
- **拓扑画布**：分层 Clos 布局 + Pod/机柜 compound 分组框；按角色语义化设备图标（内联 SVG），右上角实时状态角标（绿=运行且 BGP 全建立 / 橙=BGP 未收敛 / 灰=暂停 / 红=异常）；所有链路统一细的近黑灰（`#303133`）静态实线，选中设备蓝色边框并联动高亮直连链路，停止设备及其链路置灰；观测推送走**增量更新**（结构未变时原地刷新图标/类，不重置用户缩放/拖拽）；节点抽屉展示配置 + 实时观测（运行状态、BGP 会话 x/y、路由数、接口 up x/y、最后观测时间），抽屉非模态（开着也能继续操作画布）。
- **Web 终端**：双击设备节点弹出可拖拽/缩放/最大化的悬浮多标签 xterm.js 终端（交换机/路由器进 vtysh，server 进 bash），经 WebSocket 对接容器内 PTY（creack/pty + `docker exec -it`）；最小化停靠为右下角胶囊（会话数 + hover 列表 + 后台活动指示点）。
- **启停控制**：页头一键启停数据中心按钮 + 节点抽屉设备启停按钮。
- 前端加入 Playwright（devDependency），用于 UI 验证截图与后续 E2E。

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

## 与设计文档的已知偏差

1. **Profile 定义在代码中**（`internal/topology/defaults.go`），`profiles/*.yaml` 暂未落盘，待支持自定义 Profile 时再引入，避免双份定义。
2. **管理网 IP 未在模型中分配**：containerlab 自管管理网络，管理 IP 暂未回填（Observer 已就绪，后续可补采）。
3. **Observer 会纠正 phase**：突破设计"Observer 只写 Observed 状态"的原则，漂移时自动改写 node/lab phase（有意为之，换取免人工干预的状态收敛）。
4. **采集周期**：设计 §13 的 2/3/5s 分表合并为 2s（容器状态）+ 6s（BGP/路由/接口）两档。
5. **Operation 进度仍为 HTTP 轮询**：WebSocket 目前只覆盖拓扑观测，Operation 推送后续可迁移。

## 下一步计划（按优先级）

1. **Server Agent 与 Program 框架**（Iteration 2）：Compute Server 镜像、`dcnetlab-server-agent`、Program 安装/启停/日志、`lab-app` HTTP/TCP/UDP 模式。可顺带解决 server 访问外网需求（external 节点做 NAT 出口 + BGP default-originate 逐级下发，镜像需带 iptables）。

## 环境备注

- 开发机：WSL2，Go 1.23+、Node 24、Docker 27、containerlab 0.77（SUID + `clab_admins`，免 sudo）；内核支持 bonding/8021q/macvlan/bridge vlan_filtering。
- 运行入口：`make up` / `scripts/dcnetlab up [--dev]`（推荐），或 `make run`（仅后端）+ `make web-dev`（前端开发模式）。
