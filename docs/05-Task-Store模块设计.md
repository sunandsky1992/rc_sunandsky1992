# API 通知系统 - Task Store 模块设计

## 模块定位

Task Store 是系统的持久化层，基于 PostgreSQL，存储通知任务和供应商配置。为 Ingestion API 提供写入能力，为 Dispatcher 提供任务抢占和状态更新能力。

---

## 表结构

### vendor_configs（供应商配置表）

存储各供应商的 HTTP 协议配置。Header 和 Body 由业务方在请求中提交，不需要在此表配置模板。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | serial PK | 自增 ID |
| `vendor_id` | varchar, unique | 供应商标识，如 `ad_system` |
| `target_url` | varchar | 目标 API 地址 |
| `http_method` | varchar | GET / POST / PUT |
| `timeout_ms` | int | HTTP 超时时间 |
| `created_at` | timestamp | 创建时间 |
| `updated_at` | timestamp | 更新时间 |

### notifications（通知任务表）

存储业务方提交的通知任务，记录投递状态和回执信息。

| 字段 | 类型 | 说明 |
|------|------|------|
| `id` | bigserial PK | 内部自增 ID |
| `notification_id` | uuid, unique | 对外唯一 ID，供去重和查询 |
| `vendor_id` | varchar | 关联 vendor_configs |
| `idempotency_key` | varchar, unique | 业务方幂等键，防重复提交 |
| `headers` | jsonb | 业务方提交的 Header，投递时发送给外部 API |
| `payload` | jsonb | 业务方提交的 Body，投递时发送给外部 API |
| `status` | varchar | pending / in_flight / retrying / delivered / dead |
| `retry_count` | int | 当前重试次数 |
| `next_retry_at` | timestamp, nullable | 下次重试时间，指数退避用 |
| `response_status` | int, nullable | 回执：外部 API 返回的 HTTP 状态码 |
| `response_body` | text, nullable | 回执：外部 API 返回的响应体 |
| `created_at` | timestamp | 创建时间 |
| `updated_at` | timestamp | 更新时间 |

> **回执字段说明**：`response_status` 和 `response_body` 不参与业务逻辑（题目明确说业务系统不关心返回值），纯粹用于排查问题。每次投递（含重试）会覆盖更新为最近一次的响应。

---

## 任务抢占

Dispatcher 轮询时使用 `SKIP LOCKED` 抢占任务，多 Worker 并发安全：

```sql
SELECT * FROM notifications
WHERE status = 'pending'
   OR (status = 'retrying' AND next_retry_at <= NOW())
ORDER BY created_at
LIMIT 1
FOR UPDATE SKIP LOCKED;
```

被锁住的任务自动跳过，不会重复领取。

---

## 为什么选 PostgreSQL

| 理由 | 说明 |
|------|------|
| `SKIP LOCKED` | 原生支持任务抢占，多 Worker 并发安全 |
| JSONB | `headers` 和 `payload` 直接存 JSON，读写方便 |
| ACID | 任务落盘可靠，不怕崩 |
| Go 生态 | `pgx` 驱动成熟 |

## 不用 PostgreSQL 的替代方案

| 替代 | 能不能用 | 代价 |
|------|---------|------|
| SQLite | MVP 能用 | 无 `SKIP LOCKED`，并发弱，只能单 Worker |
| MySQL 8.0+ | 能用 | 也有 `SKIP LOCKED`，但 JSON 操作不如 PG 的 JSONB |
| Redis | 能做队列 | 失去 ACID，查询和管理不方便，宕机有丢数据风险 |
