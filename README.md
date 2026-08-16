# rc_sunandsky1992 - API 通知系统

可靠投递外部 HTTP 通知的内部服务。接收业务系统的通知请求，异步投递到不同供应商的 API，支持失败重试和死信管理。

## 对问题的理解

**表面需求**：接收业务系统的通知请求，转发到外部供应商 API。

**本质问题**：这是"不可控网络 + 不可控对端"场景下的**可靠投递**问题。业务系统与外部供应商之间隔着不可信的网络边界——网络可能抖动、外部 API 可能 5xx、进程可能崩溃、对端可能长时间不可用。本系统的价值不在于"转发一个 HTTP 请求"，而在于把业务系统的一次同步调用，转化为一个**持久化、可重试、可查询、可排查的投递任务**，把不可靠的部分关进系统内部，对外只暴露一个简单的异步契约。

对问题的拆解：

1. **不丢是底线，不重是目标，但两者不可兼得**。分布式环境下 exactly-once 需要分布式事务或两阶段提交，代价极高；而通知场景的实际代价模型是"漏投 >> 重复"——漏一次永久丢失业务事件，重复一次供应商可自行去重。因此 at-least-once 是唯一理性的起点，重复投递通过 `notification_id` 交给供应商幂等兜底。

2. **失败必须分类，重试不是万能药**。4xx 表示请求本身有问题（鉴权失败、参数错误），重试一万次结果相同，只会浪费流量、污染日志；只有 5xx、超时、网络错误才值得重试，并用指数退避拉开间隔，避免失败风暴。区分可重试 / 不可重试错误，是可靠性工程的基本功。

3. **崩溃恢复是"不丢"承诺的兜底**。任务被领取后 Worker 崩溃，任务会永远卡在 `in_flight`——没有超时回收机制，任何一次崩溃都等于静默丢消息。`updated_at` 超时（60s）后重新可抢占，守住了这条底线。

4. **幂等拦截要放在最前面**。业务系统可能因自身超时重试提交，同一事件被提交两次。在接收端用 `idempotency_key` 唯一约束拦截，比投递后去重更早、更高效、且不依赖供应商配合。

5. **对外契约要显式、可预期**。返回 202 即"已接收并持久化"，业务系统据此判断重试自己的请求是安全的；状态查询是获取最终结果的唯一途径。语义清晰，上游才能正确使用。

6. **不可观测就不可运维**。外部 API 返回了什么、重试了几次、卡在哪个状态——没有回执和统计，死信无法定位是"目标地址不可达"还是"鉴权失败"。回执字段（`response_status` / `response_body`）和 Statistics 模块不是加分项，是排障的必要条件。

本系统的定位是**投递通道**：业务系统只管提交，不关心外部 API 是否可达、格式如何、失败了怎么办；系统负责把通知可靠地送到目标地址。

## 整体架构与核心设计

```
业务系统 → Ingestion API → PostgreSQL (Task Store) → Dispatcher → 外部供应商 API
```

| 模块 | 职责 |
|------|------|
| Ingestion API | 接收通知、校验、入库、查询、死信管理、统计 |
| Task Store | PostgreSQL 持久化，SKIP LOCKED 任务抢占 |
| Dispatcher | 轮询、构建请求、投递、重试、死信处理 |
| Statistics | 成功率、失败率、重试分布统计 |

### 核心设计

**投递语义：至少一次（at-least-once）**
- 任务落库后才返回 202，进程崩溃不丢
- 失败自动重试，可能重复投递
- 重复投递风险由外部供应商处理（我们提供 `notification_id` 供去重）
- 不用 exactly-once：分布式 exactly-once 代价极高，通知场景偶尔重复的代价远小于漏投递

**任务抢占：SELECT ... FOR UPDATE SKIP LOCKED**
- 多 Worker 并发安全，被锁的任务自动跳过
- 含 in_flight 超时恢复：Worker 崩溃 60s 后任务重新可被抢占

**失败处理：指数退避 + 死信**
- 2xx → delivered
- 5xx / 超时 / 网络错误 → 指数退避重试（1s→2s→4s→...→128s，最多 8 次）
- 4xx → dead（请求本身有误，重试无意义）
- 超过最大重试次数 → dead，支持手动重投

**幂等去重**
- 业务方提交通知时携带 `idempotency_key`
- 相同 key 的重复提交直接返回已有结果，不重复入库

**供应商适配**
- `vendor_configs` 表存 URL、Method、超时等 HTTP 协议配置
- Header 和 Body 由业务方在请求中直接指定，系统透传投递

## 关键工程决策与取舍

| 决策 | 选择 | 不选的 | 理由 |
|------|------|--------|------|
| 投递语义 | at-least-once | exactly-once | 偶尔重复代价远小于漏投递 |
| 任务队列 | DB 轮询 + SKIP LOCKED | Kafka/RabbitMQ | MVP 少一个中间件，DB 轮询够用 |
| 4xx 处理 | → dead | → retry | 4xx 重试无意义，需人工排查 |
| 服务架构 | 单服务 | 微服务拆分 | MVP 拆了增加运维成本和调用链路 |
| 可观测性 | stdout 日志 | Prometheus+ELK | 先用日志，问题暴露再补 |
| 断路器 | 不做 | Hystrix/Resilience4j | 指数退避已够，后续多供应商再加 |
| 分布式事务 | 不做 | 两阶段提交 | at-least-once + 幂等足够 |
| 统计 | SQL 实时聚合 | 物化视图/缓存 | MVP 直查，流量大了再优化 |

> 详细设计文档见 [docs/](docs/) 目录（9 篇），AI 使用说明见 [09-AI使用说明](docs/09-AI使用说明.md)。

## 快速开始

### 前置条件

- Go 1.25+（gin v1.12 / pgx v5.10 要求）
- PostgreSQL 12+

### 初始化数据库

```bash
psql -U postgres -d notifications -f migrations/001_init.sql
```

### 启动服务

```bash
export DATABASE_URL="postgres://postgres:postgres@localhost:5432/notifications?sslmode=disable"
export PORT=8080
go run cmd/server/main.go
```

无数据库时会自动降级为 Mock 模式（数据不持久化）。

## API 接口

### 提交通知

```bash
curl -X POST http://localhost:8080/api/notifications \
  -H "Content-Type: application/json" \
  -d '{
    "vendor_id": "ad_system",
    "idempotency_key": "order_123_register",
    "headers": {"Authorization": "Bearer xxx"},
    "payload": {"user_id": "12345", "event": "register"}
  }'
```

### 查询状态

```bash
curl http://localhost:8080/api/notifications/{notification_id}
```

### 死信管理

```bash
# 查看死信
curl http://localhost:8080/api/dead-letters

# 重投死信
curl -X POST http://localhost:8080/api/dead-letters/{id}/retry
```

### 统计

```bash
curl "http://localhost:8080/api/stats?start=2026-08-16T00:00:00Z&end=2026-08-16T23:59:59Z"
curl "http://localhost:8080/api/stats/by-vendor?start=...&end=..."
curl "http://localhost:8080/api/stats/retry-distribution?start=...&end=..."
```

## 运行测试

```bash
go test ./... -v
```

## 项目结构

```
├── cmd/server/main.go          # 入口
├── internal/
│   ├── api/                     # HTTP 接收层
│   ├── model/                   # 数据模型
│   ├── store/                   # DB 层（PG 实现 + Mock）
│   └── dispatcher/              # 投递 Worker
├── migrations/001_init.sql      # 建表 SQL
├── docs/                        # 设计文档（9 篇）
└── README.md
```

## 设计文档

| 文档 | 内容 |
|------|------|
| [01-系统边界](docs/01-系统边界.md) | 做什么、不做什么、设计原则 |
| [02-可靠性与失败处理](docs/02-可靠性与失败处理.md) | 投递语义、失败策略、状态流转 |
| [03-取舍与演进](docs/03-取舍与演进.md) | 不采纳的过度设计、演进路线 |
| [04-Ingestion-API模块设计](docs/04-Ingestion-API模块设计.md) | 接口设计、幂等设计 |
| [05-Task-Store模块设计](docs/05-Task-Store模块设计.md) | 表结构、SKIP LOCKED、PG 选型 |
| [06-Dispatcher模块设计](docs/06-Dispatcher模块设计.md) | 轮询、投递、重试、退避 |
| [07-Statistics模块设计](docs/07-Statistics模块设计.md) | 统计接口、SQL 实现 |
| [08-中间件选型说明](docs/08-中间件选型说明.md) | 技术栈选型与替代方案 |
| [09-AI使用说明](docs/09-AI使用说明.md) | AI 帮了什么、没采纳什么、自己决策了什么 |
