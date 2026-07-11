# DCNetLab

面向数据中心物理网络和虚拟网络的本地化、可视化、可交互模拟平台。详见 [需求文档](doc/PRD.md) 和 [技术设计文档](doc/design.md)。

## 当前状态

已实现设计文档 Iteration 0–1 的核心闭环（声明式 Plan/Apply）：

```
Create Lab (Micro/Standard Profile)
      ↓
Plan:自动分配 ASN、Loopback、点到点 /31、机柜 VLAN /24,生成变更预览
      ↓
Apply:编译 FRR 配置 + Containerlab 拓扑 → 写入 Generation 快照 → 部署
      ↓
Web UI 查看拓扑(分层 Clos 视图 + Pod/机柜分组框 + 统一配色链路)、节点/链路详情、双击设备进入终端、DC/设备启停、Operation 进度
```

接入层为机柜（rack）模型：每机柜两台 leaf 组成 MLAG 对，作为 VRRP 多活网关（vlan1000 上各配物理 IP + 共享虚拟网关 IP/MAC）；server 以 bond 双归两台 leaf（access VLAN1000），固定 ASN 65000，与 leaf 的 **vlanif 物理 IP**（非 VRRP 虚 IP）建 eBGP；leaf 侧为 server 配置 BGP peer-group（listen range），机柜 ASN = 4200080000 + rack 序号。leaf 对 server 只 default-originate 一条默认路由并做出方向前缀过滤（fabric 明细不出接入层），server 路由表保持极简：本机柜直连 /24 + 静态默认（经 VRRP 虚 IP，主）+ BGP 默认（经两台 leaf 物理 IP ECMP，备）。

- **后端**：Go 模块化单体（`cmd/controller`），基于 [Kratos](https://go-kratos.dev/) 框架；API 以 protobuf 定义（`api/dcnetlab/v1/dcnetlab.proto`）并通过 `google.api.http` 注解同时提供 REST（8080）与 gRPC（9090）两种传输；SQLite 持久化，异步 Operation。
- **分配器**：IPAM（管理各地址池，支持回收/恢复）、按角色分段的 ASN 分配器（leaf 按机柜计算、server 固定 65000）。
- **编译器**：资源模型 → FRR 配置（eBGP、`redistribute connected`、ECMP、/31 互联、leaf vlanif+VRRP+server peer-group、server bond0 BGP）+ Containerlab 拓扑 YAML（leaf VLAN 网桥 / VRRP macvlan / server bond 均为 exec 产物）；全部为编译产物，资源模型是唯一事实来源。所有非 server 节点（router 角色）exec 时移除 containerlab 注入的管理网默认路由，避免 BGP 撤路由后流量静默经管理网逃逸，保证"设备停止"故障隔离语义可信。
- **Runtime Driver**：`containerlab`（检测到二进制时自动启用）/ `noop`（仅生成产物，便于无 containerlab 环境开发）。
- **部署校验**：Apply 自带 `ValidateControlPlane`（全部 BGP 会话 Established）与 `ValidateDataPlane`（server ping VRRP 网关 + server 互 ping）步骤，noop 运行时自动跳过；已在真实 containerlab 环境验证 Micro 拓扑（26 条 BGP 会话、VRRP 主备与故障切换、ECMP）。
- **前端**：Vue 3 + TypeScript + Pinia + Element Plus + Cytoscape.js，Labs / Topology / Operations 三个页面；vue-i18n 国际化（简体中文/English，默认跟随系统语言，可在侧边栏切换）。
- **Web 终端**：拓扑图上双击设备节点，弹出可拖拽/缩放的悬浮多标签 xterm.js 终端（交换机/路由器进 vtysh，server 进 bash），经 WebSocket（`/ws/v1/labs/{labId}/nodes/{nodeId}/terminal`）对接容器内 PTY；需 containerlab 运行时且 lab 已部署。
- **启停控制**：一键启停整个数据中心与设备粒度启停，统一为 docker pause/unpause 冻结语义 —— 保留 veth 接线与设备配置，停止即静默（邻居 BGP/VRRP 按真实故障收敛），启动即秒级解冻；停止的设备与其相关链路在拓扑图上置灰。
- **Observer 状态采集**：周期采集容器运行态（2s）与 BGP 会话/路由数/接口状态（6s，单次 exec 组合脚本），回填 Node observed 状态并经 WebSocket（`/ws/v1/labs/{labId}/topology`）实时推送；拓扑节点显示状态角标（绿=运行/橙=BGP 未收敛/灰=暂停/红=异常），节点抽屉展示真实 BGP 会话、路由数、接口状态；观测到手动 pause、宿主机重启等造成的状态漂移会自动纠正 node/lab phase。
- **拓扑链路配色**：所有链路（fabric / server access / MLAG peer）统一为细的近黑灰色静态实线，点击只加粗不变色；选中设备边框为蓝色，并联动高亮其全部直连链路；红/绿等强调色留给后续链路质量/故障展示。
- **一键启停**：`scripts/dcnetlab up|down|status|logs`（或 `make up` / `make down`），同时管理后端 Controller 和前端 UI。

尚未实现（按设计文档迭代顺序推进）：Server Agent 与 Program/Daemon 框架、Traffic、AF_PACKET 抓包、故障注入、可视化扩容、VPC/VXLAN、EIP。

## 快速开始

依赖：Go 1.23+、Node 20+、Docker（部署真实拓扑还需 [containerlab](https://containerlab.dev)）。

一键启动（推荐）：

```bash
make up             # 构建后端 + 前端,Controller 托管 UI,打开 http://127.0.0.1:8080
make down           # 一键停止
scripts/dcnetlab up --dev   # 开发模式:Controller + Vite 热更新(http://localhost:5173)
scripts/dcnetlab status     # 查看运行状态
scripts/dcnetlab logs       # 跟踪 Controller 日志
```

环境变量：`DCNETLAB_LISTEN`（默认 `127.0.0.1:8080`）、`DCNETLAB_RUNTIME`（`auto|containerlab|noop`）。

手动方式：

```bash
make build test                              # 构建并测试后端
./bin/dcnetlab-controller --data-dir data    # 启动 Controller
make web-install && make web-dev             # 前端开发模式,代理 /api 到 8080
```

修改 protobuf API 后重新生成代码（首次先安装工具链）：

```bash
make init            # 安装 buf / protoc-gen-go / protoc-gen-go-grpc / protoc-gen-go-http / kratos CLI / wire
make api             # buf generate:从 api/ 下的 proto 重新生成 pb/ 下的 Go 代码
make wire            # 修改依赖注入 provider 后重新生成 cmd/controller/wire_gen.go
```

proto 依赖（`google/api` 注解）由 buf 从 BSR 拉取（`buf.yaml` + `buf.lock` 锁定），仓库不落 third_party；生成代码统一输出到 `pb/`，与 proto 源文件分离。

API 冒烟：

```bash
curl -X POST localhost:8080/api/v1/labs -d '{"name":"demo","profile":"micro"}'
curl -X POST localhost:8080/api/v1/labs/<labId>/plans
curl -X POST localhost:8080/api/v1/plans/<planId>/apply
curl localhost:8080/api/v1/operations/<opId>
```

## 目录结构

```
api/dcnetlab/v1/       protobuf API 定义(REST + gRPC 的唯一事实来源)
pb/dcnetlab/v1/        buf generate 生成的 Go 代码(消息 / gRPC / Kratos HTTP)
buf.yaml, buf.gen.yaml buf 模块与代码生成配置(google/api 依赖经 BSR 锁定于 buf.lock)
cmd/controller/        Controller 入口(main + wire.go / wire_gen.go 装配 Kratos 应用)
internal/conf/         运行时配置(flag 填充,注入各层 provider)
internal/biz/          业务层,按模块拆分 usecase(lab/plan/topology/operation)+ 各自的 Repo 接口
internal/data/         数据层,与 biz 同构拆分(lab/plan/topology/operation/runtime),持有全部 SQLite 操作
internal/service/      protobuf 服务实现(pb ↔ model 转换、错误映射)
internal/server/       Kratos HTTP/gRPC 传输装配(含 Web UI 托管)
internal/model/        资源模型(唯一事实来源)
internal/allocator/    IPAM 与 ASN 分配器
internal/topology/     Profile → 节点/链路构建
internal/compiler/     FRR 配置与 Containerlab 拓扑编译(Golden 测试)
internal/operation/    异步 Operation 执行器
internal/runtime/      Runtime Driver(containerlab / noop)
web/                   Vue 3 前端(src/i18n 为国际化文案)
scripts/dcnetlab       一键启停脚本
scripts/init-tools.sh  protobuf/Kratos 工具链初始化(版本固定)
doc/                   PRD 与技术设计文档
```

目录组织遵循 [kratos-layout](https://github.com/go-kratos/kratos-layout)：biz / data / service / server 四层各有一个同名 go 文件（`biz.go`、`data.go`、`service.go`、`server.go`）持有该层的 wire `ProviderSet`，`cmd/controller/wire.go` 汇总各层 ProviderSet 生成注入代码。

## 测试与代码质量

```bash
go test ./...          # 单元 + API 集成测试
make lint              # golangci-lint 静态检查(规范见 doc/golang-style.md),要求零告警
make golden            # 模板变更后重新生成编译器 Golden 文件
```
