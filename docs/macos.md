# macOS 适配原理（OrbStack 转发运行时）

一句话原理：macOS 上不模拟 Linux，而是把整个运行入口**透明转发**进一台 OrbStack Linux 虚机——repo 经 `/Users` 同路径共享，命令、路径、产物零改动，`make up` 在 mac 和 Linux 上是同一条命令、同一份脚本。

## 为什么必须进虚机

containerlab 搭建拓扑依赖一整套 Linux 内核能力：netlink、网络命名空间、veth 对，以及本项目用到的 bonding、8021q、macvlan、vlan_filtering 网桥。Darwin 内核没有这些 API。

mac 上的任何容器方案（Docker Desktop、OrbStack、colima）本质都是"docker CLI 在 mac、容器在一台 Linux 虚机里"。但只把 docker API 转发过去并不够：containerlab 要在宿主侧直接做 netlink 操作把 veth 塞进容器命名空间，**必须贴着容器所在内核跑**。Controller 理论上可以与数据面分离，但当前实现有四处宿主耦合：以子进程直接调 `containerlab`/`docker` CLI、编译产物以宿主绝对路径作 bind mount 源、经管理网直拨 server 容器内 agent 的 gRPC，以及 agent 反向按管理网网关地址回拉软件包（假设 Controller 就在网关侧）。拆开需要宿主侧运行时代理、产物传输通道与双向网络打通——那是多宿主 / 多 DC 演进时的形态，单机实验场景收益为零，所以适配方案是整体搬进虚机，而不是逐调用远程翻译。

选 OrbStack 的原因：它把 `/Users` 以**相同路径**挂载进虚机（virtiofs），并给每台机器一个 mac 侧可解析的 `<machine>.orb.local` 域名——前者让仓库零同步携带，后者让浏览器直连虚机里的 Controller。

## 总体架构

```text
macOS                                        OrbStack 虚机「dcnetlab」(Ubuntu)
─────                                        ────────────────────────────────

浏览器 ──── http://dcnetlab.orb.local:8080 ────► web 服务 (绑定 0.0.0.0:8080,反代 controller)
                                                   │  REST / gRPC / WebSocket
make up                                            │
  └─ scripts/dcnetlab (Darwin 检测)                 ├─ Runtime Driver → containerlab
       └─ orb -m dcnetlab … ──────────────────► 同一脚本在虚机内继续执行
                                                   │     └─ docker: FRR / server 容器
                                                   │          (netns / veth / bond / VRRP)
~/Code/dc-net-lab ◄════ /Users virtiofs 同路径共享 ════► /Users/…/dc-net-lab
  (repo、bin/、.run/、data/ 双侧同一视图)                  (go build、bind mount 用同一路径)
```

`/Users` 同路径共享是整个方案的支点：repo、编译产物 `bin/`、运行状态 `.run/`、SQLite 数据 `data/` 在两侧是同一份文件；Controller 编译 containerlab 拓扑时写下的绝对路径（如 server 容器 bind mount 的 `bin/dcnetlab-node-agent`）在虚机内原样有效，`status`/`logs`/`down` 在哪侧执行都看到同一状态。

## 命令转发链路

`scripts/dcnetlab` 开头的 `maybe_exec_in_orb` 按下图判定（`up`/`down`/`status`/`logs`/`edge-image` 全部经过它）：

```text
scripts/dcnetlab <cmd>
  │
  ├─ uname != Darwin ──────────────► 本地执行（Linux 原生）
  ├─ DCNETLAB_RUNTIME=noop ────────► 本地执行（仅生成产物，不部署）
  ├─ 未装 OrbStack / 机器不存在 ────► 本地执行 + 提示 make orb-setup（运行时降级 noop）
  │
  └─ 其余 ──► orb -m dcnetlab env \
                DCNETLAB_LISTEN=0.0.0.0:8080 \
                DCNETLAB_ADVERTISE=dcnetlab.orb.local:8080 \
                scripts/dcnetlab <cmd>        ← 同一脚本、同一路径，在虚机内从头执行
```

转发时改写两个环境变量：`DCNETLAB_LISTEN` 改绑 `0.0.0.0`（默认 `127.0.0.1` 在虚机里 mac 浏览器够不着）；`DCNETLAB_ADVERTISE` 让脚本回显的 URL 用 `.orb.local` 域名而非虚机内部地址。`DCNETLAB_RUNTIME` 原样透传。

一个实现细节：`orb` 在连接终端时会查询并重置终端模式（远程会话切 raw mode），表现为清屏吞掉此前输出、或换行只回车不换行的"阶梯文本"。转发时把 stdin 接 `/dev/null`、stdout 走管道（`| cat`）绕开——被转发的命令都不读终端输入。

## 装机与 docker 挂载排序

`scripts/orb-setup` 幂等装机：创建 Ubuntu 机器，装 Docker（含把登录用户加进 docker 组，Controller 要直连 `docker.sock`）、containerlab、Go、Node，每项检测到已存在即跳过，可重复执行。

其中一项针对虚机重启的容器损坏场景：OrbStack 的 `/Users` virtiofs 在 systemd 视野之外**异步**挂载，而 docker 默认开机即启并按 restart 策略拉起容器；若容器先于共享就绪启动，源文件还不存在的单文件 bind mount 会被 docker 静默建成**空目录**，server 容器里的 agent 二进制从此挂空、自愈永远失败。orb-setup 给 docker.service 装 drop-in，用有界 `ExecStartPre` 等挂载就绪：

```text
虚机重启（无 drop-in）                      虚机重启（有 drop-in）
────────────────────                      ────────────────────
docker 启动                                docker 等待 mountpoint -q /Users（≤60s）
  └─ 容器按 restart 策略拉起                 │
       └─ bind mount 源文件不存在            /Users virtiofs 挂载就绪
            → 静默建成空目录                  └─ docker 启动
              agent 二进制挂空,无法自愈            └─ 容器拉起,bind mount 正常
（/Users 挂载姗姗来迟,为时已晚）
```

注意：drop-in 只堵住空挂载；虚机重启后 containerlab 建的 veth 对仍会丢失（容器只剩管理口），拓扑需要重新 Plan + Apply 恢复（`RestorePrograms` 会把包与程序全部装回），详见 [progress.md](progress.md) 关键经验第 3 条。

## 前端依赖的双平台共存

mac 与虚机共用同一份 `web/node_modules`，但 rollup 与 esbuild 的原生二进制按平台发布在 optional package 里（`@rollup/rollup-darwin-arm64`、`@esbuild/linux-arm64` 等），一侧 `npm install` 会把另一侧的原生包剪除。`scripts/dcnetlab` 的 `ensure_web_natives` 只把**缺失平台**的原生包下载到临时目录再合并进树，不动其余依赖，两侧构建互不打架。

## 降级链与报错指引

- 未装 OrbStack、机器不存在、或显式 `DCNETLAB_RUNTIME=noop` 时保持 mac 本地运行，运行时降级 `noop`（Plan/Apply 只生成 FRR 配置与拓扑产物，不部署），脚本打印安装提示。
- noop 运行时下操作 server 程序时，biz 层把驱动的 `ErrNotSupported` 翻译为带解法的错误（装 containerlab，或 macOS 上 `make orb-setup` 后 `make down && make up`），避免裸露 `operation not supported by this runtime driver` 这类无从下手的报错。
