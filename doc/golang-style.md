# Golang 代码质量与规范

> 本文档是仓库的 Go 代码规范基线，由 `make lint` 强制执行：机械规则走 `.golangci.yml`
> （golangci-lint），空行语义规则（块结束后空行、return 前空行、禁止连续空行）走
> `scripts/check-style.py`；其余依赖判断的部分在 code review 中执行。

## 1. 编码格式与工具链

* 必须遵循官方 [Effective Go](https://go.dev/doc/effective_go) 规范。
* 必须使用 `go fmt` 或 `goimports` 进行代码格式化。
* 变量和常量命名遵循驼峰命名法，例如 `myVariable`。
* 私有变量禁止使用下划线前缀。
* 代码排版应保持清晰，通过空行分隔不同语义的代码块。
* 变量初始化、参数校验、主要处理逻辑、错误处理和结果返回之间，应根据语义使用空行分隔。
* 不要将多个不同职责的语句连续堆叠在一起。
* 简单且紧密相关的连续赋值可以保持在同一代码块中，无需机械插入空行。

示例：

```go
func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	config := &Config{}
	if err := json.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	return config, nil
}
```

## 2. 错误处理与控制流

* 严格遵循 `if err != nil` 提前返回原则，保持主逻辑扁平化。
* 错误信息必须使用 `fmt.Errorf` 包装上下文，并通过 `%w` 保留原始错误，例如：

```go
return nil, fmt.Errorf("failed to open file: %w", err)
```

* 不允许使用 `panic` 或 `os.Exit` 处理常规业务逻辑。
* 条件检查、错误处理、循环、`switch` 和主要业务处理之间，应使用空行区分不同的语义块。
* 一个完整的 `if`、`for` 或 `switch` 块结束后，除非有特殊需求，否则应增加一个空行。
* 函数末尾的最终 `return` 前应保留一个空行，使返回结果与前面的处理逻辑清晰分隔。
* 提前返回语句属于对应条件块的一部分，不要求在 `return` 前额外增加空行。

推荐：

```go
func findUser(ctx context.Context, id string) (*User, error) {
	if id == "" {
		return nil, errors.New("user id is empty")
	}

	user, err := repository.GetUser(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}

	if !user.Enabled {
		return nil, ErrUserDisabled
	}

	return user, nil
}
```

不推荐：

```go
func findUser(ctx context.Context, id string) (*User, error) {
	if id == "" {
		return nil, errors.New("user id is empty")
	}
	user, err := repository.GetUser(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	if !user.Enabled {
		return nil, ErrUserDisabled
	}
	return user, nil
}
```

对于仅包含简单返回逻辑的短函数，不需要为了形式强制增加空行：

```go
func (u *User) Name() string {
	return u.name
}
```

## 3. 接口与面向对象

* 保持接口定义小而聚焦，遵循接口隔离原则。
* 接口应由调用方根据实际需要定义，避免提前设计宽泛接口。
* 优先返回具体类型或具体类型指针，不要为单一实现无意义地定义接口。
* 避免使用空接口 `interface{}`；在支持泛型的 Go 版本中，应使用 `any` 表示任意类型。
* 不要为了模拟传统面向对象继承体系而引入复杂的嵌套和抽象层。
* 构造函数、依赖校验和主要初始化逻辑之间，应通过空行分隔不同处理阶段。

示例：

```go
func NewService(repository Repository, logger Logger) (*Service, error) {
	if repository == nil {
		return nil, errors.New("repository is nil")
	}

	if logger == nil {
		return nil, errors.New("logger is nil")
	}

	service := &Service{
		repository: repository,
		logger:     logger,
	}

	return service, nil
}
```

## 4. 并发与测试

* 启动 Goroutine 时，必须明确协程的生命周期、退出条件和资源回收方式。
* 应通过 `context.Context` 传递取消信号。
* 多个 Goroutine 需要统一等待时，应使用 `sync.WaitGroup`、`errgroup.Group` 或仓库已有的并发管理方式。
* 禁止启动没有明确退出机制的 Goroutine。
* 禁止通过无界 Goroutine 或无界 Channel 堆积任务。
* 所有公开函数和核心业务逻辑必须编写对应的单元测试。
* 单元测试基于标准库 `testing`，断言工具沿用仓库已有选择。
* 多组输入验证相同行为时，优先使用表驱动测试。
* 测试代码同样遵循语义块空行分隔规则。
* 测试准备、执行和断言三个阶段之间，应使用空行分隔。

示例：

```go
func TestService_GetUser(t *testing.T) {
	repository := newMockRepository()
	service := NewService(repository)

	user, err := service.GetUser(context.Background(), "user-1")

	require.NoError(t, err)
	require.Equal(t, "user-1", user.ID)
}
```

## 5. 空行与代码块规范

* 空行用于表达代码语义边界，而不是单纯增加视觉间距。
* 以下不同阶段之间通常应保留一个空行：
  * 参数和前置条件校验；
  * 数据读取或依赖调用；
  * 数据转换和业务处理；
  * 状态更新或副作用操作；
  * 最终结果返回。
* 连续且属于同一目的的变量声明或赋值不需要空行分隔。
* 相邻的多个参数校验可以根据复杂度分别分块；每个校验逻辑较完整时，应使用空行分隔。
* 一个代码块结束后，后续进入新的处理阶段时应保留空行。
* 函数末尾的最终 `return` 前通常保留一个空行。
* `if err != nil` 内部的提前 `return` 不需要与错误判断之间增加空行。
* 不允许连续使用两个或更多空行。
* 不要为了满足该规则而破坏短函数的紧凑性。
* 最终排版以代码语义清晰、与仓库现有代码风格一致为准。

推荐：

```go
func process(ctx context.Context, request *Request) (*Result, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}

	input, err := loadInput(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("failed to load input: %w", err)
	}

	normalized := normalize(input)
	result := calculate(normalized)

	if err := saveResult(ctx, result); err != nil {
		return nil, fmt.Errorf("failed to save result: %w", err)
	}

	return result, nil
}
```
