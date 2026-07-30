# DCNetLab

数据中心网络仿真实验平台：控制面 controller（Kratos，protobuf API）+ 数据面 agent（驱动 Containerlab）+ Vue 3 前端，三进程经 gRPC/HTTP 通信（见 docs/architecture.md）。

## 常用命令

- `make init` — 安装工具链（buf / protoc 插件 / kratos / wire / golangci-lint，版本固定在 scripts/init-tools.sh）
- `make api` — 从 api/ 下的 proto 重新生成 pb/ 下的代码（buf generate）
- `make wire` — 修改 provider 后重新生成 controller/cmd/controller/wire_gen.go
- `make build test lint` — 构建、测试、静态检查；提交前三者必须全部通过
- `make golden` — 编译器模板变更后更新 golden 基线
- `make images` — 重建节点镜像（dcnetlab/frr、dcnetlab/server、frr-edge）

## 代码规范

Go 代码必须遵循 [docs/golang-style.md](docs/golang-style.md)：

- gofmt/goimports 格式化；`make lint` 必须零告警（golangci-lint 见 .golangci.yml，
  空行语义由 scripts/check-style.py 检查）
- 错误用 `fmt.Errorf("...: %w", err)` 包装上下文；`if err != nil` 提前返回，主逻辑扁平
- 空行分隔语义块：参数校验、依赖调用、业务处理、副作用、最终返回之间空一行；
  完整的 if/for/switch 块结束后除特殊需求外一律空一行（任意嵌套层级）；
  函数末尾 return 前空一行；短函数不强行加空行；禁止连续两个空行
- 接口由调用方定义、小而聚焦（见 biz 的 Repo 接口）；单一实现不预设接口
- goroutine 必须有明确退出机制（context / 超时）；公开函数与核心逻辑要有单测，多组输入用表驱动

Commit message 必须遵循 [docs/git-commit-style.md](docs/git-commit-style.md)：

- Conventional Commits 格式 `<type>(<scope>): <summary>`；type 用 feat/fix/refactor/perf/test/docs/style/build/ci/chore/revert，禁用 update/change/modify 等模糊词
- summary 用英文祈使语气、首字母小写、结尾无句号、≤72 字符，描述行为变化而非文件改动
- scope 用稳定模块名（ipam/topology/fabric/api/ui 等），不用文件名或函数名；跨模块可省略
- 一个提交只表达一个主要逻辑变更；diff 含多个独立变更时建议拆分；生成前必须先分析实际 diff
- 不兼容变更在 type/scope 后加 `!` 并在 footer 写 BREAKING CHANGE 说明

中文文档（docs/、README、注释、PR 描述等）必须遵循 [docs/chinese-doc-style.md](docs/chinese-doc-style.md)：

- 中文与英文、数字之间加一个半角空格；数字与单位之间加空格（保持仓库既有风格优先）
- 中文句子用全角标点（，。；：等），英文句子内部用半角标点；全角标点两侧不加空格
- 专有名词用官方大小写（Go/GitHub/Kubernetes，不写 Golang/github/K8S）
- 代码、命令、路径、标识符用反引号包裹；代码块、日志、URL 等固定文本保持原样
- 避免与当前任务无关的大范围排版 diff；风格冲突时只改任务直接涉及的内容

## 架构约束

- 按组件分目录：controller/（控制面）、agent/（数据面 agent + clab 驱动）、
  nodeapps/（交付进仿真容器的应用），三者的 internal 互不可见；
  跨面共享只有 api/（proto 契约）、pb/ 与根 internal/（model/nodeagentapi/runtime）
- controller 不直接接触 docker/containerlab，运行时操作一律经 runtime.Driver
  （agentdriver → 数据面 agent gRPC；noop 降级）
- 控制面内分层严格单向：service → biz → data；service 不触达数据层，biz 不解析 HTTP/protobuf
- 每层同名文件（biz.go/data.go/service.go/server.go）持有该层 wire ProviderSet；
  biz 与 data 按功能模块同构拆分（lab/plan/topology/operation）
- API 以 api/ 下的 proto 为唯一事实来源（dcnetlab/agent/nodeagent 三个服务），
  生成代码在 pb/（勿手改）
- internal/model 是零依赖的共享资源模型；模块私有类型放各自包内
- 新功能落地时同步更新 docs/progress.md
