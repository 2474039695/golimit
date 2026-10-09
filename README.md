# Golimit

基于 Redis + Lua 的分布式令牌桶，提供 Gin、GoFrame 中间件和 Prometheus 指标。每个 key 独立保存额度，支持突发流量和按请求成本扣减。

## 阅读顺序

第一次接入先看“安装与准备”和“Gin 快速开始”或“GoFrame 快速开始”。需要给不同接口不同速率时看“不同接口使用不同速率”。参数细节、降级和监控在后面的进阶章节；项目文件结构与旧版本迁移说明在文末。

## 安装与准备

- Go 版本至少为 1.25.1，与本仓库的 `go.mod` 要求一致。
- 准备一个可连接的 Redis。下面的完整示例使用 `127.0.0.1:6379`，没有密码；按你的环境修改地址、密码和 DB。
- 在业务项目中安装依赖：

如果是新项目，先在业务项目目录执行 `go mod init example.com/golimit-demo`；已有 `go.mod` 的项目跳过这一步。然后安装：

```bash
go get github.com/2474039695/golimit
```

本文对应当前仓库代码。如果修改尚未发布，`go get` 下载的版本可能没有新 API。在本地验证时，可以在业务项目的 `go.mod` 加上下面这一行，让导入指向本地仓库：

```go
replace github.com/2474039695/golimit => D:/project/golimit
```

然后执行 `go mod tidy`。该路径适用于当前 Windows 工作区，其他电脑需改为实际路径。下面标有“完整程序”的示例各自独立；每次选择一个保存为 `main.go`，不要把多个 `main` 函数放在同一个包里。

## 先理解四个步骤

| 步骤 | 代码入口 | 作用 |
| --- | --- | --- |
| 1. 创建 Redis 客户端 | `redis.NewClient` | 创建应用共用的 Redis 客户端 |
| 2. 定义额度 | `limiter.NewRedis(rdb, cfg)` | 配置补充速率 Rate 和最大容量 Capacity |
| 3. 生成中间件 | `middleware.NewGinWithOptions` / `NewGoFrameWithOptions` | 决定规则名、按谁限流、存储故障时如何处理 |
| 4. 绑定路由 | 框架路由 API | 在业务处理函数之前执行限流 |

`Rate` 和 `Capacity` 决定“额度多少”；`PolicyName` 和 `KeyFunc` 决定“谁共用这份额度”。

例如 `Rate: 1, Capacity: 3` 表示每秒补充 1 个令牌，最多积累 3 个。新桶初始是满的，每次默认扣 1 个；令牌不够时返回 429。它允许短时间连续请求，不是“任意一秒严格最多 1 次”。

## Gin 快速开始：单接口按 IP 限流

这是完整程序。它只限流 `GET /ping`，每个 IP 独立拥有每秒补充 1 个、容量为 3 的桶。

```go
package main

import (
    "log"
    "time"

    "github.com/2474039695/golimit/limiter"
    "github.com/2474039695/golimit/middleware"
    "github.com/gin-gonic/gin"
    "github.com/redis/go-redis/v9"
)

func main() {
    // 第 1 步：创建 Redis 客户端。整个应用可以共用这一份客户端。
    rdb := redis.NewClient(&redis.Options{
        Addr:                  "127.0.0.1:6379",
        ContextTimeoutEnabled: true, // 让请求截止时间参与 Redis 网络读写。
        MaxRetries:            -1,   // 响应丢失时不重复扣减令牌。
        DialTimeout:           time.Second,
        ReadTimeout:           time.Second,
        WriteTimeout:          time.Second,
    })
    defer rdb.Close()

    // 第 2 步：定义额度。每秒补充 1 个令牌，最多存 3 个。
    // 每次请求默认消耗 1 个令牌，所以满桶可以连续放行 3 次。
    cfg := limiter.Config{Rate: 1, Capacity: 3}
    base, err := limiter.NewRedis(rdb, cfg)
    if err != nil {
        log.Fatal(err)
    }

    // 第 3 步：生成中间件。这里按直连客户端 IP 分开计算额度。
    // nil 表示没有可信代理；部署在 Nginx 等代理后时，需要配置代理网段。
    ipKey, err := middleware.ClientIPKey(nil)
    if err != nil {
        log.Fatal(err)
    }
    limitHandler, err := middleware.NewGinWithOptions(base, middleware.Options{
        PolicyName:  "ping_v1",             // 固定规则名，区分不同接口的桶。
        KeyFunc:     ipKey,                 // 返回身份，每个 IP 有独立的桶。
        ErrorPolicy: middleware.FailClosed, // Redis 出错时返回 503。
    })
    if err != nil {
        log.Fatal(err)
    }

    // 第 4 步：把中间件放在业务处理函数前面，只限流 GET /ping。
    router := gin.New()
    router.Use(gin.Recovery())
    router.GET("/ping", limitHandler, func(c *gin.Context) {
        c.JSON(200, gin.H{"message": "pong"})
    })
    log.Fatal(router.Run(":8080"))
}
```

保存后执行 `go mod tidy`，再执行 `go run .`。在另一个终端请求：

```bash
curl -i http://localhost:8080/ping
```

Windows PowerShell 使用 `curl.exe -i http://localhost:8080/ping`。短时间连续请求同一地址可以观察拒绝响应；请求间隔较长时，桶会在请求过程中恢复令牌。

| 状态码 | 本例中的含义 |
| --- | --- |
| 200 | 允许请求，执行了业务处理函数 |
| 429 | 令牌不够，业务处理函数不会执行；可查看 Retry-After |
| 503 | Redis 故障或限流判断超时，FailClosed 策略阻止了请求 |

这个示例的 Redis key 形如 `golimit:ping_v1:127.0.0.1`。IP 来自连接地址；不同机器、IPv4 和 IPv6 可能形成不同身份。限流器构造函数只校验配置，不会连接 Redis；连接不可用时，本例首次请求会得到 503。需要启动时检查 Redis 的应用，可自行执行带超时的 PING。

### 想改变限流范围，改哪里？

| 需求 | 配置方式 |
| --- | --- |
| 所有请求共用额度 | 不设置 KeyFunc，同一 PolicyName 只有一个桶 |
| 每个 IP 独立额度 | 使用本例的 ClientIPKey；经过代理时配置可信代理网段 |
| 每个用户独立额度 | KeyFunc 返回前置认证得到的用户 ID |
| 多个接口共用额度 | 把同一中间件绑定到这些接口或路由组 |
| 不同接口不同速率 | 分别创建不同配置、不同 PolicyName 的中间件，见下一节 |

未设置 `KeyFunc` 时，`PolicyName: "ping_v1"` 对应的键是 `golimit:ping_v1`，所有访问该规则的请求共用额度。设置 `KeyFunc` 后，返回的身份会自动拼接到规则名称后面，不需要在业务处理函数里再调用 `Allow`。

## 不同接口使用不同速率

给每套规则分别创建限流器和中间件，再挂到对应接口。Redis 客户端仍然共用一份。

| 接口 | 规则名称 | 每秒补充令牌 Rate | 桶容量 Capacity | 按谁区分 |
| --- | --- | ---: | ---: | --- |
| POST /login | login_v1 | 1 | 3 | 客户端 IP |
| GET /products | products_v1 | 20 | 40 | 客户端 IP |

这些数值用于演示，应按业务需要设置。下面是完整程序，替换前面的 `main.go` 即可运行：

```go
package main

import (
    "log"
    "time"

    "github.com/2474039695/golimit/limiter"
    "github.com/2474039695/golimit/middleware"
    "github.com/gin-gonic/gin"
    "github.com/redis/go-redis/v9"
)

func main() {
    // 两套规则共用 Redis 客户端。
    rdb := redis.NewClient(&redis.Options{
        Addr:                  "127.0.0.1:6379",
        ContextTimeoutEnabled: true,
        MaxRetries:            -1,
        DialTimeout:           time.Second,
        ReadTimeout:           time.Second,
        WriteTimeout:          time.Second,
    })
    defer rdb.Close()
    ipKey, err := middleware.ClientIPKey(nil)
    if err != nil {
        log.Fatal(err)
    }

    // newRule 是示例里自己写的辅助函数，不是库提供的新 API。
    // 只在启动时调用：每套规则创建一次限流器和中间件。
    newRule := func(name string, rate float64, capacity int64) (gin.HandlerFunc, error) {
        l, err := limiter.NewRedis(rdb, limiter.Config{
            Rate:     rate,
            Capacity: capacity,
        })
        if err != nil {
            return nil, err
        }
        return middleware.NewGinWithOptions(l, middleware.Options{
            PolicyName:  name,
            KeyFunc:     ipKey,
            ErrorPolicy: middleware.FailClosed,
        })
    }

    // 同一个 IP 的登录额度和商品查询额度互不占用。
    loginLimit, err := newRule("login_v1", 1, 3)
    if err != nil {
        log.Fatal(err)
    }
    productsLimit, err := newRule("products_v1", 20, 40)
    if err != nil {
        log.Fatal(err)
    }

    router := gin.New()
    router.Use(gin.Recovery())
    router.POST("/login", loginLimit, func(c *gin.Context) {
        // 演示响应；实际项目在这里接入自己的登录逻辑。
        c.JSON(200, gin.H{"message": "login handler reached"})
    })
    router.GET("/products", productsLimit, func(c *gin.Context) {
        c.JSON(200, gin.H{"products": []string{"book", "pen"}})
    })
    log.Fatal(router.Run(":8080"))
}
```

同一个 IP 的键分别是 `golimit:login_v1:IP` 和 `golimit:products_v1:IP`，两个桶独立恢复和扣减。

- 创建限流器、IP 提取函数和中间件都放在启动阶段，请求时直接复用。
- 只换 PolicyName 会分开桶，但不会改变 Rate；不同速率需要使用不同 Config。
- 同一 Redis key 必须使用相同 Rate/Capacity。不同规则使用不同 PolicyName；修改已有规则的速率时，可使用新的版本名，如 login_v2，新桶会从满额开始。
- 多个应用实例需要共享同一规则时，应使用相同 KeyPrefix、PolicyName、身份提取方式和 Rate/Capacity。
- 如果把规则挂到全局，又挂到单个路由，一次请求会经过两次限流。只需要接口独立额度时，按示例绑定到具体路由即可。

## GoFrame 快速开始

使用 GoFrame 时，创建额度规则的步骤相同，只把适配函数换成 `middleware.NewGoFrameWithOptions`。下面是独立的完整程序，保存为另一个项目的 `main.go`，或替换前面的 Gin 程序后运行。

```go
package main

import (
    "log"
    "time"

    "github.com/2474039695/golimit/limiter"
    "github.com/2474039695/golimit/middleware"
    "github.com/gogf/gf/v2/frame/g"
    "github.com/gogf/gf/v2/net/ghttp"
    "github.com/redis/go-redis/v9"
)

func main() {
    // 第 1 步：创建 Redis 客户端。整个应用可以共用这一份客户端。
    rdb := redis.NewClient(&redis.Options{
        Addr:                  "127.0.0.1:6379",
        ContextTimeoutEnabled: true, // 让请求截止时间参与 Redis 网络读写。
        MaxRetries:            -1,   // 响应丢失时不重复扣减令牌。
        DialTimeout:           time.Second,
        ReadTimeout:           time.Second,
        WriteTimeout:          time.Second,
    })
    defer rdb.Close()

    // 第 2 步：定义额度。每秒补充 1 个令牌，最多存 3 个。
    // 每次请求默认消耗 1 个令牌，所以满桶可以连续放行 3 次。
    cfg := limiter.Config{Rate: 1, Capacity: 3}
    base, err := limiter.NewRedis(rdb, cfg)
    if err != nil {
        log.Fatal(err)
    }

    // 第 3 步：生成中间件。这里按直连客户端 IP 分开计算额度。
    // nil 表示没有可信代理；部署在 Nginx 等代理后时，需要配置代理网段。
    ipKey, err := middleware.ClientIPKey(nil)
    if err != nil {
        log.Fatal(err)
    }
    limitHandler, err := middleware.NewGoFrameWithOptions(base, middleware.Options{
        PolicyName:  "ping_v1",             // 固定规则名，区分不同接口的桶。
        KeyFunc:     ipKey,                 // 返回身份，每个 IP 有独立的桶。
        ErrorPolicy: middleware.FailClosed, // Redis 出错时返回 503。
    })
    if err != nil {
        log.Fatal(err)
    }

    // 第 4 步：将中间件全局挂载，再注册业务路由。
    // 本例只有 /ping；新增接口也会使用同一套规则。
    server := g.Server()
    server.SetAddr(":8080")
    server.Use(limitHandler)
    server.BindHandler("/ping", func(r *ghttp.Request) {
        r.Response.WriteJson(map[string]string{"message": "pong"})
    })
    server.Run()
}
```

本例使用 `server.Use(handler)` 全局挂载，所有接口按同一个 IP 桶共享额度。Gin 的对应写法是 `router.Use(limitHandler)`。如果要给不同接口不同速率，应分别创建规则并绑定到对应路由或路由组；不要把某个接口的规则挂到整个服务。

GoFrame 也可接入 Prometheus，具体见后面的监控章节。

## 进阶：Redis 客户端与规则配置

以下为配置片段，放在应用启动流程中；完整程序见前面的快速开始。

```go
rdb := redis.NewClient(&redis.Options{
    Addr:                  "localhost:6379",
    ContextTimeoutEnabled: true, // 让 context 的 deadline 参与 socket 读写。
    MaxRetries:            -1,   // 禁止网络失败后重复执行扣减。
    DialTimeout:           time.Second,
    ReadTimeout:           time.Second,
    WriteTimeout:          time.Second,
})
defer rdb.Close()

cfg := limiter.Config{
    Rate:      10,
    Capacity:  20,
    KeyPrefix: "golimit",
    Timeout:   50 * time.Millisecond,
    IdleTTL:   time.Hour,
    MaxTTL:    7 * 24 * time.Hour,
}
l, err := limiter.NewRedis(rdb, cfg)
if err != nil {
    log.Fatal(err)
}
```

这里的 50 毫秒只是接入示例，需要根据 Redis 网络延迟与业务预算设置。创建函数不访问网络；Redis 可达性检查由应用启动流程自行执行，例如有超时的 PING。

| 配置 | 约束与默认值 |
| --- | --- |
| Rate | 必须大于 0，不能是 NaN 或无穷大，允许小数 |
| Capacity | 1 到 2^53-1 的整数 |
| KeyPrefix | 默认 golimit，最多 128 字节，不能含空白或控制字符 |
| Timeout | 默认 1 秒，负值无效；请求的更短 deadline 优先 |
| IdleTTL | 默认 1 小时，至少 1 秒 |
| MaxTTL | 默认 7 天，至少 1 秒，必须覆盖 IdleTTL 和完整补满周期 |

TTL 计算为 `max(ceil(IdleTTL秒数), ceil(Capacity/Rate)+1)`，放行与拒绝请求都会刷新。MaxTTL 不够时创建失败，不会直接截短 TTL：否则桶会在恢复前过期，重建成满桶。动态 key 最多 512 字节，不能为空或含空白、控制字符；count 必须介于 1 和 Capacity。

Go 校验后 Lua 再校验参数。同一 key 必须使用相同 Rate/Capacity；脚本检测到冲突返回配置错误。旧桶首次访问时补齐配置字段与时间余量，不能追回旧脚本已丢失的时间。修改速率或容量应使用新的规则版本/命名空间，注意新桶会从满额开始；这不是热更新接口。

## 动态身份、请求成本与可信代理

### 中间件选项怎么选？

下面这些字段属于 `middleware.Options`；Rate/Capacity 属于 `limiter.Config`，不能在这里设置。

| 选项 | 是否需要填写 | 用途 |
| --- | --- | --- |
| PolicyName | 必填 | 固定规则名，如 login_v1；只能包含字母、数字、下划线、点或短横线，长度 1～64 |
| KeyFunc | 可选 | 返回用户、IP 或租户身份；不填写则该规则共用一个桶 |
| CostFunc | 可选 | 每次扣几个令牌；不填写时扣 1 个 |
| SkipFunc | 可选 | 返回 true 的请求跳过限流，如健康检查 |
| ErrorPolicy | 可选 | 默认为 FailClosed；故障策略见下一节 |
| Fallback | 按策略填写 | 选择 LocalFallback 时必须提供本地限流器 |
| Timeout | 可选 | 整个限流判断的时间预算，默认为 2 秒 |
| OnError | 可选 | 记录限流异常的回调，如接入日志 |

### 按用户限流与自定义成本

Options.KeyFunc、CostFunc、SkipFunc 都接收标准 `*http.Request`，Gin 与 GoFrame 可复用。认证中间件必须在用户维度限流之前执行。

下面是填写 Options 时的字段片段，不是完整程序。`authenticatedTenantAndUser` 和 `validatedBatchSize` 需要由业务实现：

```go
// userID/tenantID 应从前置认证后的 Request.Context 读取。
// 不要直接相信客户端声明的用户或租户 header。
KeyFunc: func(r *http.Request) (string, error) {
    return authenticatedTenantAndUser(r.Context())
},
CostFunc: func(r *http.Request) (int64, error) {
    return validatedBatchSize(r.Context())
},
```

上面的两个函数由业务实现，例如返回 `tenant:100:user:200` 和经校验的批量大小。身份缺失或成本无效返回 400，不进入存储故障放行策略；401/403 应由前置认证处理。避免在回调里重复读取消耗请求 body。

IP 模式可通过 `middleware.ClientIPKey([]netip.Prefix{...})` 指定代理网段。只有 RemoteAddr 属于可信代理，才从右向左解析 X-Forwarded-For 并取第一个非代理地址。代理必须追加或覆盖这个 header；没有代理配置时忽略客户端传入的 IP header。不使用 0.0.0.0/0 等全信任范围。

## 故障策略与总预算

| 模式 | Redis 判定失败后的行为 |
| --- | --- |
| FailClosed | 默认模式，返回 503 |
| FailOpen | 继续业务，返回 X-RateLimit-Degraded: true，不生成虚假的配额信息 |
| LocalFallback | 用本进程的有限本地桶再判断，放行或返回 429；本地槽位不足返回 503 |

正常额度不足才是 429。错误细节通过 OnError 记录，HTTP 响应不泄露 Redis 地址或错误内容。请求已取消或中间件总预算耗尽时统一停止处理，不能继续业务；KeyFunc/CostFunc 非法输入返回 400，配置冲突返回 500。

下面是本地降级的配置片段，沿用基础示例中的 `base` 和 `ipKey`。创建中间件时使用这里的 `opts`：

```go
local, err := limiter.NewLocal(
    limiter.Config{Rate: 2, Capacity: 4, IdleTTL: time.Minute},
    limiter.LocalOptions{MaxKeys: 10000},
)
if err != nil { log.Fatal(err) }
// 创建 WithOptions 中间件时设置：
opts := middleware.Options{
    PolicyName: "public_api",
    KeyFunc: ipKey,
    ErrorPolicy: middleware.LocalFallback,
    Fallback: local,
    Timeout: 100 * time.Millisecond,
}
handler, err := middleware.NewGinWithOptions(base, opts)
if err != nil { log.Fatal(err) }
// 把 handler 绑定到需要限流的路由；GoFrame 改用 NewGoFrameWithOptions。
```

本地桶使用 Go 单调时钟，内存最多 MaxKeys 个桶。过期节点用最小堆清理，满槽时不淘汰仍有效的桶，避免轮换身份获得新的满额。没有后台 goroutine，闲置桶在后续访问时惰性清理，内存始终有上限。

本地降级是每进程独立的备用配额，N 个实例可能总计放行约 N 份配额，重启也会恢复满桶。对必须严格共享额度的业务应选择 FailClosed。结果未知时 Redis 可能已扣减，本地兜底是额外的保守判定，不会回滚 Redis 额度。OnError 可连接应用日志/告警；回调必须快速且并发安全。包装器指标记录后端判定，不等同于最终业务是否执行，fail-open/fallback 事件要结合 OnError 观察。

Config.Timeout 约束一次 Redis 判断；Options.Timeout 约束整个限流阶段（含兜底），默认 2 秒，调用方更短的 deadline 优先。短预算不会传递给后续正常业务处理。所有自定义回调/后端必须遵守 context；组件无法强制终止不配合的用户代码或取消已经在 Redis 中运行的脚本。

Redis 网络超时可能意味着脚本已经执行但响应丢失，返回 BackendError，errors.Is 可同时判断 ErrBackend 与底层 deadline 错误。不要自行重试扣减。仅 NOSCRIPT 明确表示尚未执行，才允许自动回退 EVAL。

## 详细结果与响应头

```go
res, err := base.Check(ctx, "public_api:user:1001", 1)
if err != nil {
    // 按业务策略处理错误，不直接重复执行。
}
```

Result 包含 Allowed、Capacity、Remaining、RetryAfter、ResetAfter、Reason、Metadata。Remaining 是本次扣减后可立即使用的完整令牌；RetryAfter 是当前状态下再次尝试相同成本需等待的时间；ResetAfter 是补满时间。等待值不是预留承诺，期间其他请求可能抢先消耗额度。

中间件只有在 Metadata=true 时发送：

- X-RateLimit-Limit：桶容量，不是每秒速率。
- X-RateLimit-Remaining：剩余完整令牌。
- X-RateLimit-Reset-After：距离补满的秒数，向上取整，为本库自定义头。
- Retry-After：429 时的重试等待秒数，向上取整。

旧式自定义 Limiter 仍能包装并接入，但只能返回 bool，无法生成上述详细额度信息。

## Prometheus 指标

通过 NewPrometheus 创建包装器，在启动时指定固定 PolicyName。标签只含 policy、backend（redis/local/custom）、result（allow/deny/error）和固定的 error_code 分类，没有原始 key、IP、用户 ID 或错误文本。

同一 Registry/Namespace/Subsystem 默认最多 100 个 policy，MaxPolicies 可调整；超出时报错，相同 policy 不允许配置冲突。固定规则名也不能在每次请求里创建：每个规则应在启动时只创建一次包装器。

| 指标 | 标签 | 含义 |
| --- | --- | --- |
| golimit_limiter_checks_total | policy,backend,result | 后端判定次数 |
| golimit_limiter_requested_tokens_total | policy,backend | 请求的正令牌数，包含拒绝和错误 |
| golimit_limiter_consumed_tokens_total | policy,backend | 明确成功扣减的令牌数；未知结果不计入 |
| golimit_limiter_errors_total | policy,backend,error_code | 按有限分类统计错误 |
| golimit_limiter_check_duration_seconds | policy,backend | 后端判定耗时直方图 |
| golimit_limiter_config_rate | policy | 固定规则的每秒速率 |
| golimit_limiter_config_capacity | policy | 固定规则的桶容量 |

consumed 表示已确认的成功扣减；超时结果未知时它不能反映 Redis 的精确总消耗，不适合计费。NewPrometheus 处理注册错误并复用已有采集器；旧 WrapPrometheus 在创建失败时会 panic。

迁移时替换旧 requests_total、request_tokens_total、allow_duration_seconds 名称，并将 by(key) 改为 by(policy)。用 requested 与 consumed 区分请求需求和已确认消耗。

```promql
sum(rate(golimit_limiter_checks_total{result="deny"}[5m])) by (policy)
sum(rate(golimit_limiter_errors_total[5m])) by (policy, error_code)
histogram_quantile(0.95,
  sum(rate(golimit_limiter_check_duration_seconds_bucket[5m])) by (le, policy))
```

使用自定义 Registry 时必须配合 HandlerFor(registry)，默认 Handler 只读取默认 Registry。

### 可选：Gin 同时接入 Prometheus

下面是另一份完整程序，同时展示限流和监控。阅读完基础示例后再使用它：将基础限流器包装为 monitored，再把 monitored 交给中间件；业务请求仍只执行一次扣减。

```go
package main

import (
    "log"
    "net/http"
    "time"

    "github.com/2474039695/golimit/limiter"
    "github.com/2474039695/golimit/metrics"
    "github.com/2474039695/golimit/middleware"
    "github.com/gin-gonic/gin"
    "github.com/prometheus/client_golang/prometheus"
    "github.com/redis/go-redis/v9"
)

func main() {
    rdb := redis.NewClient(&redis.Options{
        Addr: "localhost:6379", ContextTimeoutEnabled: true, MaxRetries: -1,
        DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second,
    })
    defer rdb.Close()

    cfg := limiter.Config{Rate: 10, Capacity: 20, Timeout: 50 * time.Millisecond}
    base, err := limiter.NewRedis(rdb, cfg)
    if err != nil { log.Fatal(err) }

    registry := prometheus.NewRegistry()
    monitored, err := metrics.NewPrometheus(base, cfg, metrics.Options{
        PolicyName: "public_api", Registerer: registry, MaxPolicies: 100,
    })
    if err != nil { log.Fatal(err) }

    // nil 表示只认直连地址。部署在代理后方时，需显式配置可信代理网段。
    ipKey, err := middleware.ClientIPKey(nil)
    if err != nil { log.Fatal(err) }
    handler, err := middleware.NewGinWithOptions(monitored, middleware.Options{
        PolicyName: "public_api",
        KeyFunc: ipKey,
        ErrorPolicy: middleware.FailClosed,
        Timeout: 100 * time.Millisecond,
        SkipFunc: func(r *http.Request) bool { return r.URL.Path == "/metrics" },
        OnError: func(_ *http.Request, err error) { log.Printf("限流判定异常: %v", err) },
    })
    if err != nil { log.Fatal(err) }

    router := gin.New()
    router.Use(gin.Recovery(), handler)
    router.GET("/metrics", gin.WrapH(metrics.HandlerFor(registry)))
    router.GET("/ping", func(c *gin.Context) { c.JSON(200, gin.H{"message": "pong"}) })
    log.Fatal(router.Run(":8080"))
}
```

中间件自动拼出 `PolicyName:KeyFunc返回值`，Redis 再加 KeyPrefix。示例键形如 `golimit:public_api:127.0.0.1`。KeyFunc 未设置时，整个规则共享一个桶。

## Cluster 与时间源

当前支持 go-redis Client（含 Sentinel failover 返回的 Client）和 ClusterClient；无法检查选项的自定义客户端包装器在创建时会被拒绝。

Cluster 严格模式要求 ContextTimeoutEnabled=true、MaxRetries=-1、MaxRedirects=-1、ReadOnly=false。go-redis 的 MaxRedirects 循环不仅处理 MOVED/ASK，也可能重试网络错误；关闭它避免不确定的重复扣减，但本次 MOVED/ASK 会报错并交由故障策略处理，不能保证迁移期间所有请求都成功。该行为需要在你的实际集群迁移/故障切换演练中验证。

每次脚本只访问 KEYS[1]，不存在多 key 跨槽原子操作。现在还没有实现多层配额联合扣减；简单串联中间件会产生“前一个桶已扣减，后一个桶拒绝”的行为。

时间仍由 Go 传入。脚本防御时钟回退，但应用时钟向前跳仍可能提前补充。Redis TIME、全局时钟同步、Cluster 故障切换时钟偏差不在本次变更中；部署时需要保持节点时钟同步。旧版本实例不能与新脚本长期混用同一 key，因为旧脚本不会更新 remaining_ms 和配置字段。

## 验证

```bash
go test ./...
go vet ./...
go test -race ./...
```

自动测试覆盖参数/TTL边界、时间余量、满桶、时钟回退、旧状态迁移、并发扣减、真实 TCP 响应丢失、调用方更短 deadline、动态身份/成本、跳过规则、三种故障策略、本地容量以及指标基数和计数语义。Lua 的常规测试使用 miniredis；生产上线前还需在实际 Redis 版本/Cluster 上验证故障切换与负载预算。windows/386 目标不支持 -race，本次已用 GOARCH=amd64、CGO_ENABLED=1 和 64 位 gcc 成功运行竞态测试。

可选真实 Redis 测试通过环境变量指定专用实例，只创建和删除带唯一前缀的测试键：

```powershell
$env:GOLIMIT_TEST_REDIS_ADDR = "127.0.0.1:6379"
go test ./limiter -run TestRealRedisIntegration -count=1 -v
```

## 项目结构

```text
golimit/
├── limiter/                         # 限流接口、配置与后端实现
│   ├── limiter.go                   # Limiter / DetailedLimiter / Result
│   ├── config.go                    # 配置默认值、参数校验与 TTL 计算
│   ├── errors.go                    # 错误分类与 BackendError
│   ├── redis.go                     # Redis 客户端校验与脚本调用
│   ├── token_bucket.lua             # 原子令牌桶算法，使用 go:embed 内嵌
│   ├── local.go                     # 有容量上限的进程内令牌桶
│   ├── config_test.go               # 配置与 TTL 边界测试
│   ├── redis_test.go                # Redis 后端行为及共享测试辅助函数
│   ├── token_bucket_test.go         # Lua 补充令牌、时间与状态边界测试
│   ├── local_test.go                # 本地桶容量与过期清理测试
│   ├── redis_timeout_test.go        # Redis 网络超时、响应丢失与请求截止时间
│   └── redis_integration_test.go    # 可选的真实 Redis 集成测试
├── middleware/                      # Gin / GoFrame 适配及共享 HTTP 逻辑
│   ├── gin.go / gin_test.go         # Gin 适配与测试
│   ├── goframe.go / goframe_test.go # GoFrame 适配与测试
│   ├── options.go                   # 公共选项、故障策略与配置校验
│   ├── decision.go                  # 动态键、请求成本及故障策略判断
│   ├── response.go                  # 响应头与错误提示
│   └── ip.go / ip_test.go           # 可信代理与客户端 IP 提取
├── metrics/                         # Prometheus 包装器与测试
│   ├── prometheus.go
│   └── prometheus_test.go
├── go.mod
├── go.sum
└── README.md
```

测试文件与被测源码放在同一包目录，使用 `_test.go` 后缀；Go 正常构建不会将这些测试文件编译进库。Lua 算法测试使用 miniredis，真实 Redis 测试单独命名并通过环境变量启用。本次按职责拆分文件，包名、公开 API 和导入路径保持不变。

### 文件调整说明

| 原文件 | 调整后的文件 | 职责 |
| --- | --- | --- |
| `limiter/limiter.go` | `limiter.go`、`config.go`、`errors.go` | 分开接口及结果类型、配置校验、错误类型 |
| `limiter/script.lua` | `limiter/token_bucket.lua` | 明确脚本使用令牌桶算法，`redis.go` 的内嵌路径已同步更新 |
| `limiter/config_test.go` | `config_test.go`、`redis_test.go`、`local_test.go` | 将配置、Redis 后端、本地桶测试归入各自文件 |
| `limiter/redis_test.go` 中的脚本测试 | `limiter/token_bucket_test.go` | 集中验证 Lua 算法的令牌补充与时间边界 |
| `limiter/timeout_test.go` | `limiter/redis_timeout_test.go` | 明确测试 Redis 调用的超时行为 |
| `limiter/integration_test.go` | `limiter/redis_integration_test.go` | 明确测试真实 Redis 集成 |
| `middleware/options.go` | `options.go`、`decision.go`、`response.go` | 分开公共配置、限流判断、HTTP 响应处理 |
| `middleware/middleware_test.go` | `gin_test.go`、`goframe_test.go` | 分开两个框架适配器的测试 |

表中省略目录的文件名仍位于原来的包目录下。本次文件整理没有新增包，也没有修改限流算法或测试断言。

### 测试目录约定

- 单元测试通常放在被测源码旁，例如 `local.go` 对应 `local_test.go`，便于同时维护实现与测试。
- 当前测试使用与源码相同的包名，可以验证未导出的内部状态；以后只验证公开 API 的测试也可以使用 `limiter_test` 等外部测试包名，仍放在同一目录。
- `redis_integration_test.go` 通过 `GOLIMIT_TEST_REDIS_ADDR` 启用真实 Redis 测试；未设置时跳过。文件名本身不会让 Go 自动跳过集成测试。
- 后续如增加需要启动完整应用或多个服务的端到端测试，可单独设置 `tests/e2e/` 目录；当前测试按包存放即可。

## 旧版本迁移

- `limiter.NewRedis(client, cfg)` 现在返回 `(*RedisLimiter, error)`，必须在启动时检查错误。
- 旧 `limiter.New` 保留原签名，创建失败会 panic；新项目使用 `NewRedis`。
- `Allow(ctx, key, count)` 仍可用；新增 `Check` 返回剩余额度及等待时间。
- `middleware.NewGin(key, l)`、`NewGoFrame(key, l)` 保留固定键、成本 1、故障放行入口。
- 新业务使用 `NewGinWithOptions`、`NewGoFrameWithOptions`，两者均返回处理函数和 error，默认故障拒绝。
- 指标已迁移到有限标签的新版名称，旧 `key` 标签及旧指标名称不再暴露，请更新仪表盘和告警。
