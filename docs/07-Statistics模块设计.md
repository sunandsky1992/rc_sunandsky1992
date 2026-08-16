# API 通知系统 - Statistics 模块设计

## 模块定位

Statistics 模块提供投递情况的统计查询能力，基于 SQL 聚合查询直接从 `notifications` 表统计，不需要单独的统计表。

**核心职责：提供时间范围内的总数、成功率、失败率等指标查询。**

---

## 模块职责

### 属于本模块的

| 职责 | 说明 |
|------|------|
| 统计概览 | 指定时间范围内的总数、成功率、失败率 |
| 按供应商统计 | 按 vendor_id 分组的投递情况 |
| 状态分布 | 各状态（pending/delivered/dead 等）的实时数量 |
| 重试分布 | 重试次数分布（0 次成功、1 次成功、2 次...） |

### 不属于本模块的

| 不属于 | 原因 |
|------|------|
| 实时监控/告警 | MVP 用日志，后续演进再补 |
| 数据看板/可视化 | 只提供 API，前端渲染不管 |

---

## 接口设计

### 1. 统计概览

```
GET /api/stats?start=2026-08-16T00:00:00Z&end=2026-08-16T23:59:59Z
```

**响应：**

```json
{
  "time_range": {
    "start": "2026-08-16T00:00:00Z",
    "end": "2026-08-16T23:59:59Z"
  },
  "total": 1500,
  "delivered": 1420,
  "failed": 80,
  "dead": 15,
  "pending": 3,
  "in_flight": 2,
  "retrying": 5,
  "success_rate": 0.947,
  "failure_rate": 0.053
}
```

### 2. 按供应商统计

```
GET /api/stats/by-vendor?start=...&end=...
```

**响应：**

```json
[
  { "vendor_id": "ad_system", "total": 800, "delivered": 760, "dead": 5, "success_rate": 0.95 },
  { "vendor_id": "crm_system", "total": 500, "delivered": 480, "dead": 8, "success_rate": 0.96 },
  { "vendor_id": "inventory", "total": 200, "delivered": 180, "dead": 2, "success_rate": 0.90 }
]
```

### 3. 重试分布

```
GET /api/stats/retry-distribution?start=...&end=...
```

**响应：**

```json
[
  { "retry_count": 0, "total": 1300 },
  { "retry_count": 1, "total": 120 },
  { "retry_count": 2, "total": 45 },
  { "retry_count": 3, "total": 20 },
  { "retry_count": 8, "total": 15 }
]
```

---

## 实现方式

MVP 直接用 SQL 聚合查询，不需要单独的统计表。后续流量大了可以加物化视图或缓存。

### 概览查询

```sql
SELECT
  COUNT(*) AS total,
  COUNT(*) FILTER (WHERE status = 'delivered') AS delivered,
  COUNT(*) FILTER (WHERE status = 'dead') AS dead,
  COUNT(*) FILTER (WHERE status = 'pending') AS pending,
  COUNT(*) FILTER (WHERE status = 'in_flight') AS in_flight,
  COUNT(*) FILTER (WHERE status = 'retrying') AS retrying
FROM notifications
WHERE created_at BETWEEN ? AND ?;
```

### 按供应商查询

```sql
SELECT
  vendor_id,
  COUNT(*) AS total,
  COUNT(*) FILTER (WHERE status = 'delivered') AS delivered,
  COUNT(*) FILTER (WHERE status = 'dead') AS dead
FROM notifications
WHERE created_at BETWEEN ? AND ?
GROUP BY vendor_id;
```

### 重试分布查询

```sql
SELECT
  retry_count,
  COUNT(*) AS total
FROM notifications
WHERE created_at BETWEEN ? AND ?
GROUP BY retry_count
ORDER BY retry_count;
```

---

## 日志收集与深度监控（演进方向）

MVP 阶段的日志通过标准库 `log.Printf` 输出到 stdout，包含以下关键事件：

| 日志事件 | 说明 |
|---------|------|
| `notification created` | 通知创建，含 id、vendor、idempotency_key |
| `idempotency hit` | 幂等去重命中 |
| `claimed` | Dispatcher 抢占任务 |
| `delivered` | 投递成功，含状态码和耗时 |
| `deliver failed (network)` | 网络错误 |
| `deliver 4xx dead` | 4xx 进死信 |
| `retrying` | 重试安排，含下次重试时间 |
| `max retries reached, dead` | 达到最大重试次数 |
| `dead letter retried` | 死信手动重投 |

这些结构化日志可以通过日志收集系统接入专门的监控工具，实现更深度的统计和告警：

```
应用 stdout → 日志收集（如 Filebeat/Fluentd）→ 日志存储（如 Elasticsearch/Loki）→ 监控看板（如 Kibana/Grafana）
```

**通过日志可实现而 MVP 代码中不需要做的：**
- **实时告警**：如死信数量超阈值、成功率骤降时自动触发告警
- **投递耗时分布**：P50/P99 延迟统计（日志中已记录 `duration`）
- **供应商维度监控**：按 vendor 聚合成功率和错误率
- **趋势分析**：按小时/天维度的投递量趋势

**演进路线：** MVP 用 stdout 日志 → 接入日志收集系统做监控告警 → 后续可引入 Prometheus Metrics 做实时指标采集。不在 MVP 中提前建设，等真实需求驱动。
