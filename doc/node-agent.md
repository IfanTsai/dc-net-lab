# node-agent 与容器内 CLI

本文描述每台模拟 server 里运行的 `node-agent` daemon 的功能与运行逻辑，以及容器终端内的运维命令（`pkg` / `program`）。代码位于 `serverapps/` 子树（Go internal 规则保证 Controller 无法引用其实现），两侧共享的线上契约常量（端口、状态、类型、启动脚本）在 `internal/agentapi`。

## 定位与交付

`node-agent` 是每台 server 容器内的进程监督 daemon，扮演真实服务器上 systemd + 包管理器后端的角色：

- **交付**：宿主机构建产物 `dcnetlab-node-agent` / `dcnetlab-node-cli` 以单文件 bind mount 挂进容器（`/opt/dcnetlab/bin/{node-agent,node-cli}`，语义上等价于 OS 预装，无需构建镜像）；部署时 containerlab exec 执行统一的启动脚本（`agentapi.StartAgentScript`）——建状态目录、把工具软链进 `/usr/local/bin`（`node-agent`、`node-cli`、`pkg`、`program`）、后台拉起 daemon。
- **通信**：gRPC 监听管理网 `:50061`（`--listen`），Controller 是唯一远程调用方；CLI 走本机 `127.0.0.1:50061`。**没有任意命令 RPC**——agent 只会安装来自 Controller 仓库的包、监督声明过的程序。
- **自愈**：Observer 深度巡检（6 s）对 running 状态的 server 容器探测 agent 端口，拒连即通过 `docker exec` 重放同一份启动脚本。agent 被拉起即视为一次"服务器开机"（见下文 boot 语义），覆盖 agent 崩溃/OOM、宿主机挂起唤醒、容器被平台外重启等场景。

## 状态目录

单一状态根 `/opt/dcnetlab/run`（`--dir`），布局：

```
/opt/dcnetlab/run/
  agent.log                     agent 自身日志(启动脚本重定向)
  repo.url                      最近一次安装记下的仓库地址(CLI 自动发现用)
  packages/<name>/<version>/    解包后的包版本(多版本共存)
    .dcnetlab-sha256            制品摘要标记,存在即视为安装完整
  programs/<name>/
    meta.json                   程序定义 + 期望状态(spec + desired + pid)
    program.log                 程序合并输出(超 5 MiB 轮转为 .1)
```

## gRPC API（`api/nodeagent/v1`，服务 `NodeAgent`）

| RPC | 语义 |
|---|---|
| `InstallPackage` | 按 URL + SHA-256 下载制品、流式校验、防逃逸解包（拒绝 `..`/绝对路径/链接，上限 256 MiB）、原子 rename 激活；同版本同摘要幂等跳过，摘要变化（builtin 开发迭代）重新下载 |
| `RemovePackage` | 删除本地版本（指定一个或全部）；被程序引用的版本拒删，回复携带引用它的程序名 |
| `ListPackages` | 列出本地存储中校验完整的包版本（节点库存页数据源） |
| `Install` | 注册/替换程序定义（运行中需先停；校验类型、重启策略、入口存在） |
| `Start` / `Stop` | 启停程序并持久化期望状态；Stop 对进程组先 SIGTERM、5 s 后 SIGKILL |
| `Remove` | 停止并删除程序定义与日志 |
| `List` | 全部程序的实时快照（spec + 状态/PID/重启次数） |
| `TailLogs` | 程序日志尾部（默认 200 行,上限 2000 行） |

## 程序模型（systemd 语义）

| 概念 | 对应 systemd | 说明 |
|---|---|---|
| `type: simple` | `Type=simple` | 常驻服务（默认） |
| `type: oneshot` | `Type=oneshot` | 一次性任务；正常退出进入终态 `Exited`（区别于 Stopped/Failed），期望状态自动落回 Stopped；拒绝 `Always` 重启策略 |
| `autoStart` | `systemctl enable` | 每次"开机"自动启动，无论此前是否在跑 |
| `restartPolicy` | `Restart=` | `Never` / `OnFailure` / `Always`，重启退避 1 s 起倍增、封顶 15 s |
| 状态 | — | `Configured` / `Running` / `Stopped` / `Failed` / `Exited` |

## 运行逻辑

- **启动恢复（boot 语义）**：agent 启动时逐个读取 `programs/*/meta.json`，先对上一世的 PID 整组补 SIGKILL（防孤儿进程与新监督并存），然后拉起 `desired == Running` **或** `autoStart` 的程序——前者对应"监督者重启不改变运行中的服务"，后者对应"enabled 单元开机自启"（oneshot 也随每次开机重跑）。
- **监督循环**：每个运行中的程序一个 goroutine，`Setpgid` 自成进程组；退出后按顺序判定：用户已 Stop → `Stopped`；`Always` → 重启；`OnFailure` 且失败 → 重启；失败 → `Failed`；oneshot 正常退出 → `Exited`；否则 → `Stopped`。
- **优雅退出**：agent 收 SIGTERM 只杀进程、**不改动持久化的期望状态**，下一世按 boot 语义恢复原样（systemd 重启自身不会 disable 任何服务）。
- **包存储单写者**：所有包安装/删除（无论来自 Controller 还是 CLI）都经本机 daemon 的 gRPC 走同一条下载/校验/解包链路；GC 在每次安装后保留最近 3 个未被程序引用的版本。
- **托管与非托管**：Controller 创建的程序期望状态存中心库,重部署由 apply 的 `RestorePrograms` 步骤恢复；容器终端里用 `program` 命令自建的程序是**节点本地**的——agent 重启/开机自启语义齐全,但 Controller 不管理、重部署不恢复（对应真实世界绕过配置管理手写 unit），在拓扑图 server 抽屉的"程序"页可见并标注"非托管"。

## 容器内 CLI

`node-cli` 是 busybox 式多调用二进制，按 `argv[0]` 分发；部署时软链 `pkg` 与 `program` 到 `/usr/local/bin`，`pkg ...` 与 `node-cli pkg ...` 等价。退出码约定：`0` 成功、`1` 操作失败、`2` 用法错误。

### `pkg` —— 包管理（对应 UI 的 Packages 页）

```
pkg list                     列出仓库全部包及本机安装状态
pkg list <name>              列出一个包的所有版本
pkg install <name>[@<ver>]   安装(缺省装最新版;已装同摘要幂等跳过)
pkg remove <name>[@<ver>]    删除本地版本(缺省删全部未被引用的版本)
```

- 被程序引用的版本拒删；一个版本都没删掉时按错误处理（退出码 1，报错指明 `<pkg>@<ver> is in use by program <name>`），部分删除成功时保留的版本以 `kept:` 行说明。
- 仓库地址自动发现链：`--repo` → `DCNETLAB_REPO` 环境变量 → daemon 记下的 `repo.url` → eth0 所在网段首地址 + 默认端口 50062（Docker 网桥惯例）。

### `program` —— 程序管理（对应 UI 的 Programs 页，语义逐条对齐）

```
program list                        列出本机全部程序(类型/自启/策略/状态/PID)
program deploy <name> <pkg>[@<ver>] [deploy flags] [-- <args>...]
                                    注册程序(按需从仓库装包;注册后不启动)
program start <name>                启动(oneshot 即"运行一次")
program stop <name>                 停止
program remove <name>               停止并删除定义与日志
program logs <name> [-n <lines>]    查看程序日志尾部
```

deploy 标志：`--type simple|oneshot`（默认 simple）、`--auto-start`（开机自启）、`--restart-policy Never|OnFailure|Always`（默认 Never；oneshot 拒绝 Always）。程序参数放在 `--` 之后原样传入。

示例：

```
$ program deploy local-web trafficgen --restart-policy Always -- udp-server
deployed local-web (trafficgen@0.1.0, node-local); start it with: program start local-web
$ program start local-web
local-web: Running (pid 443)
```

两个命令的公共标志（放在子命令前）：`--repo <url>`、`--dir <path>`（默认 `/opt/dcnetlab/run`）、`--agent <addr>`（默认 `127.0.0.1:50061`）。
