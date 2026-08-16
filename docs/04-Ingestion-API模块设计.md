# API 通知系统 - Ingestion API 模块设计

## 模块定位

Ingestion API 是系统的入口层，负责接收业务系统的通知请求、持久化任务、提供状态查询和管理入口。

**核心职责：收 → 校验 → 存 → 返回，以及对外提供查询和管理入口。投递链路一律不管。**

---

## 模块职责

### 属于本模块的

| 职责 | 说明 |
|------|------|
| 接收请求 | 暴露 HTTP 端点，接收业务系统的提交通知 |
| 请求校验 | 校验格式、`vendor_id` 是否存在、payload 是否为空 |
| 幂等去重 | 识别业务系统的重复提交（通过幂等 key） |
| 生成唯一 ID | 生成 `notification_id`（UUID），供后续去重和状态查询 |
| 入库 | 写入 `notifications` 表，状态标记 `pending`，事务保证落盘后才返回 |
| 返回响应 | 返回 202 + notification_id |
| 状态查询 | 查询通知当前状态（pending/delivered/dead 等） |
| 死信管理 | 查看死信列表、手动触发重投 |

### 不属于本模块的（归 Dispatcher）

| 不属于 | 原因 |
|------|------|
| 重试机制 | 重试是投递阶段的事，Ingestion 只管收和存 |
| 调用外部 API | 这是 Dispatcher 的职责 |
| 供应商适配渲染 | payload → 目标 HTTP 请求格式的转换在投递时才做 |
| 状态流转驱动 | pending → in_flight → delivered 这条链由 Dispatcher 驱动 |

---

## 接口设计

### 1. 提交通知

```
POST /api/notifications
```

**请求：**

```json
{
  "vendor_id": "ad_system",
  "idempotency_key": "order_12345_register",
  "headers": {
    "Authorization": "Bearer xxx",
    "X-Custom-Header": "abc"
  },
  "payload": {
    "user_id": "12345",
    "event": "registration",
    "timestamp": "2026-08-16T12:00:00Z"
  }
}
```

| 字段 | 说明 |
|------|------|
| `vendor_id` | 指定要通知哪个供应商，对应 `vendor_configs` 表中的配置 |
| `idempotency_key` | 幂等键，业务系统生成，相同 key 视为重复提交，返回已有结果 |
| `headers` | 业务方自定义 Header，投递时与供应商配置的 `header_template` 合并，业务方 Header 优先级更高 |
| `payload` | 业务数据，原样存储，投递时按供应商配置渲染成目标请求格式 |

**响应：**

```json
HTTP 202 Accepted
{
  "notification_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "status": "pending"
}
```

**内部流程：**

1. 校验 `vendor_id` 是否存在（不存在返回 400）
2. 校验 `payload` 是否为空（为空返回 400）
3. 检查 `idempotency_key` 是否已存在（已存在则返回原结果，不重复入库）
4. 生成唯一 `notification_id`（UUID）
5. 将 `headers`、`payload` 原样写入 `notifications` 表，状态 `pending`
6. 返回 202

### 2. 查询状态

```
GET /api/notifications/:id
```

**响应：**

```json
HTTP 200 OK
{
  "notification_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "vendor_id": "ad_system",
  "status": "delivered",
  "retry_count": 0,
  "created_at": "2026-08-16T12:35:00Z",
  "updated_at": "2026-08-16T12:35:02Z"
}
```

### 3. 查看死信列表

```
GET /api/dead-letters
```

**响应：**

```json
HTTP 200 OK
[
  {
    "notification_id": "a1b2c3d4-...",
    "vendor_id": "crm_system",
    "status": "dead",
    "retry_count": 8,
    "created_at": "2026-08-16T10:00:00Z",
    "updated_at": "2026-08-16T10:05:08Z"
  }
]
```

### 4. 手动重投死信

```
POST /api/dead-letters/:id/retry
```

将状态从 `dead` 重置为 `pending`，`retry_count` 清零，重新进入投递队列。

**响应：**

```json
HTTP 200 OK
{
  "notification_id": "a1b2c3d4-...",
  "status": "pending",
  "retry_count": 0
}
```

---

## 幂等设计

- 业务系统在提交通知时携带 `idempotency_key`
- `notifications` 表对 `idempotency_key` 加唯一索引
- 相同 key 的重复提交直接返回已有结果，不重复入库、不重复投递
- 这样即使业务系统因网络超时重试，也不会产生重复通知
