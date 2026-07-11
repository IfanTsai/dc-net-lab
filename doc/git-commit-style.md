# Git Commit Message 风格规范

生成 Git Commit Message 时，必须遵循以下规则。

## 1. 基本格式

使用 Conventional Commits 风格：

```text
<type>(<scope>): <summary>

<body>

<footer>
```

其中：

* `type`：提交类型，必填。
* `scope`：影响范围，可选。
* `summary`：简洁描述本次修改，必填。
* `body`：说明修改背景、实现方式或关键影响，可选。
* `footer`：关联 Issue、Breaking Change 等信息，可选。

## 2. 提交类型

优先使用以下类型：

| Type       | 说明              |
| ---------- | --------------- |
| `feat`     | 新增功能            |
| `fix`      | 修复缺陷            |
| `refactor` | 重构代码，不改变外部行为    |
| `perf`     | 性能优化            |
| `test`     | 新增或修改测试         |
| `docs`     | 文档修改            |
| `style`    | 仅格式、空行、命名等非逻辑修改 |
| `build`    | 构建系统或依赖修改       |
| `ci`       | CI/CD 配置修改      |
| `chore`    | 日常维护、工具或辅助配置修改  |
| `revert`   | 回滚已有提交          |

不要使用含义模糊的类型，例如：

```text
update
change
modify
misc
improve
```

## 3. Summary 规范

Summary 必须：

* 使用英文。
* 使用祈使语气，例如 `add`、`fix`、`remove`、`support`、`prevent`。
* 首字母小写。
* 结尾不加句号。
* 清晰描述行为变化，而不是只描述修改了哪些文件。
* 建议不超过 72 个字符。
* 一次提交只描述一个主要目标。

推荐：

```text
feat(ipam): support rack-based server subnet allocation
fix(bgp): prevent duplicate route advertisement
refactor(topology): separate node creation from link wiring
test(address): cover exhausted address pool allocation
docs(design): clarify leaf and server gateway model
```

不推荐：

```text
update code
fix bug
modify ipam.go
some changes
refactor
```

## 4. Scope 规范

Scope 应使用稳定、明确的模块名，优先使用仓库中的领域或包名，例如：

```text
ipam
topology
fabric
bgp
server
address
capture
api
ui
config
```

Scope 应满足：

* 使用小写。
* 避免文件名作为 scope。
* 避免过细，例如不要使用具体函数名。
* 跨多个模块且无法确定主要模块时，可以省略 scope。

推荐：

```text
feat(ipam): add server address reservation
fix(capture): stop leaked packet capture sessions
```

不推荐：

```text
fix(ipam_service_go): fix allocation
fix(AllocateServerIP): handle error
```

## 5. Body 规范

当修改不够直观、涉及行为变化、架构调整或多个关键点时，应增加 Body。

Body 应重点说明：

* 为什么需要修改。
* 修改了什么核心行为。
* 是否存在兼容性、迁移或运行时影响。
* 是否增加测试或验证。

不要在 Body 中逐文件罗列修改内容，也不要重复 Summary。

示例：

```text
feat(ipam): support rack-based server subnet allocation

Allocate one server VLAN subnet per rack and derive server addresses from
the subnet associated with the connected leaf pair.

Reserve the virtual gateway and leaf VLAN interface addresses before
allocating addresses to servers.
```

## 6. Breaking Change 规范

存在不兼容变更时：

* 在 type 或 scope 后增加 `!`。
* 在 Footer 中明确说明影响和迁移方式。

示例：

```text
feat(api)!: replace address pool allocation response

Return allocation metadata instead of a plain IP address so callers can
identify the selected pool, subnet, and reservation source.

BREAKING CHANGE: clients must read the allocated address from the
`address` field of the response object.
```

## 7. 提交粒度

生成 Commit Message 前，应根据实际 diff 判断提交目标。

要求：

* 一个提交只表达一个主要逻辑变更。
* 不要把功能、重构、格式化和无关清理混在同一个 Summary 中。
* 如果 diff 包含多个互不相关的修改，应建议拆分提交。
* 如果重构是实现功能或修复所必需的，可以放在同一提交中，但 Summary 应描述最终行为变化。
* 纯格式化修改使用 `style`，不要使用 `refactor`。
* 仅增加测试使用 `test`；功能与测试同步增加时使用 `feat` 或 `fix`。

## 8. 根据 Diff 生成提交信息

生成 Commit Message 时，必须先分析实际变更，不得仅根据用户描述猜测。

重点识别：

* 外部行为是否改变。
* 是否新增能力。
* 是否修复已知错误。
* 是否只是内部结构调整。
* 是否涉及配置、接口、数据模型或兼容性变化。
* 是否存在对应测试。
* 是否包含生成文件、依赖或格式化噪声。

Commit Message 应描述变更意图和结果，而不是机械复述 diff。

例如，diff 中新增了地址池锁和并发测试，应生成：

```text
fix(ipam): prevent concurrent duplicate address allocation
```

而不是：

```text
fix(ipam): add mutex and test
```

## 9. 输出要求

默认只输出最终 Commit Message，不附加解释，不使用 Markdown 代码块。

简单提交只输出单行：

```text
feat(topology): add dual-leaf rack topology
```

复杂提交输出完整格式：

```text
refactor(ipam): separate subnet selection from address allocation

Move rack subnet selection into a dedicated allocator stage so address
reservation can be reused by server installation and dry-run workflows.

Keep the existing allocation behavior unchanged.
```

当 diff 明显包含多个独立变更时，不要强行生成一个 Commit Message，应输出建议的拆分结果，例如：

```text
Suggested commits:

1. feat(ipam): add rack-based subnet allocation
2. refactor(topology): extract leaf pair lookup
3. test(ipam): cover exhausted rack subnet pools
```

## 10. 推荐风格示例

```text
feat(fabric): add BGP sessions between leaf and spine nodes
```

```text
fix(server): assign bond address from the leaf VLAN subnet
```

```text
refactor(address): centralize reserved address calculation
```

```text
test(ipam): cover allocation across multiple racks
```

```text
docs(network): clarify server VLAN gateway ownership
```

```text
chore(deps): update Go module dependencies
```

```text
style(ipam): separate logical blocks with blank lines
```
