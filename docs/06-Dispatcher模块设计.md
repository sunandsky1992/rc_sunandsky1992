# API 通知系统 - Dispatcher 模块设计

## 模块定位

Dispatcher 是系统的投递引擎，负责从 Task Store 轮询任务、构建 HTTP 请求、调用外部供应商 API、根据结果更新任务状态。

**核心职责：轮询 → 领取 → 构建请求 → 投递 → 处理结果 → 记录回执。**

---

## 模块职责

### 属于本模块的

| 职责 | 说明 |
|------|------|
| 轮询任务 | 定期从 DB 抢占 pending 和到期的 retrying 任务（`SKIP LOCKED`） |
| 任务领取 | 锁定任务，状态更新为 `in_flight` |
| 构建 HTTP 请求 | 用 vendor_configs 的 url/method/timeout + 任务的 headers/payload 组装请求 |
| 执行投递 | 调用外部 API |
| 处理结果 | 成功 → delivered；失败 → retrying + 指数退避；超上限 → dead |
| 记录回执 | 每次投递更新 `response_status` 和 `response_body` |

### 不属于本模块的

| 不属于 | 归属 |
|--------|------|
| 接收请求 | Ingestion API |
| 死信管理 API | Ingestion API |
| DB schema 设计 | Task Store |

---

## 详细设计

### 1. 轮询任务（Poll）

**逻辑：**
- 每 1s 执行一次查询
- 抢占条件：
  - `pending` 任务
  - `retrying` 且 `next_retry_at <= NOW()`
  - `in_flight` 但 `updated_at` 超过 60s（Worker 崩溃恢复）
- 每轮查询结束（无论是否有任务）sleep 1s，再开始下一轮

**查询语句：**

```sql
SELECT * FROM notifications
WHERE status = 'pending'
   OR (status = 'retrying' AND next_retry_at <= NOW())
   OR (status = 'in_flight' AND updated_at < NOW() - INTERVAL '60 seconds')
ORDER BY created_at
LIMIT 1
FOR UPDATE SKIP LOCKED;
```

> **in_flight 超时恢复**：Worker 可能在投递过程中崩溃，任务卡在 `in_flight` 永远不动。`updated_at` 超过 60s 的 `in_flight` 任务会被重新抢占。

### 2. 任务领取（Claim）

**逻辑：**
- 在同事务中更新状态为 `in_flight`，提交事务（释放行锁）
- 后续投递在事务外执行，避免长事务

```
BEGIN
  SELECT ... FOR UPDATE SKIP LOCKED  → 拿到 task
  UPDATE notifications SET status = 'in_flight', updated_at = NOW() WHERE id = ?
COMMIT
→ 进入投递流程
```

### 3. 构建 HTTP 请求（Build）

**逻辑：**
1. 用 `task.vendor_id` 查 `vendor_configs`，拿到 `target_url`、`http_method`、`timeout_ms`
2. 组装请求：
   - URL = `vendor_configs.target_url`
   - Method = `vendor_configs.http_method`
   - Headers = `task.headers`（JSONB → http.Header）
   - Body = `task.payload`（JSONB → []byte）
3. 创建 `http.Client`，设超时 = `vendor_configs.timeout_ms`

### 4. 执行投递（Deliver）

**逻辑：**
- `client.Do(req)` 发起 HTTP 调用
- 捕获三类结果：成功响应（有 status code）、网络错误（无响应）、超时
- 无论结果如何，都要进入"处理结果"阶段更新 DB

### 5. 处理结果（Handle Result）

**分三种情况：**

| 响应 | 处理 | DB 更新 |
|------|------|---------|
| 2xx | 投递成功 | `status = 'delivered'` |
| 5xx / 超时 / 网络错误 | 可重试失败 | `retry_count++`，判断是否超上限 |
| 4xx | 请求本身有误，重试无意义 | `status = 'dead'` |

**重试判断逻辑：**

```
retry_count++
if retry_count > MAX_RETRIES(8):   // 第 9 次失败（已重试 8 次）后进 dead
    status = 'dead'
else:
    status = 'retrying'
    next_retry_at = NOW() + backoff(retry_count)
```

### 6. 记录回执（Record Response）

**逻辑：**
- 每次投递（含重试）都更新 `response_status` 和 `response_body`
- `response_status` = HTTP 状态码（网络错误/超时时记 0）
- `response_body` = 响应体（截断到 4KB，防止过大）
- 不参与业务逻辑，纯粹排查用

### 7. 退避计算（Backoff）

```
base = 1s
delay = base * 2^(retry_count - 1)
```

| 重试第 N 次 | 等待时间 |
|------------|---------|
| 1 | 1s |
| 2 | 2s |
| 3 | 4s |
| 4 | 8s |
| 5 | 16s |
| 6 | 32s |
| 7 | 64s |
| 8 | 128s |
| 9 | → dead |

### 8. 并发模型（MVP）

- 单进程，单 goroutine 轮询
- 串行投递：拿一个 → 投一个 → 更新 → 拿下一个
- 简单够用，演进时再开多 Worker

---

## 工作流程图

```
        ┌─────────────┐
        │   POLL      │ ← 每 1s 轮询一次
        └──────┬──────┘
               │ SELECT ... FOR UPDATE SKIP LOCKED
               ▼
        ┌─────────────┐
        │   CLAIM     │ → UPDATE status = 'in_flight'
        └──────┬──────┘
               │
               ▼
        ┌─────────────┐
        │  BUILD       │ → vendor_configs + headers + payload → HTTP Request
        └──────┬──────┘
               │
               ▼
        ┌─────────────┐
        │  DELIVER     │ → http.Client.Do(req)
        └──────┬──────┘
               │
       ┌───────┼───────┐
       ▼       ▼       ▼
    2xx      5xx/超时   4xx
    ↓        ↓          ↓
  delivered  retrying   dead（请求格式错误，重试无意义）
```
