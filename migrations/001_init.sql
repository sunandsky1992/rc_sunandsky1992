-- vendor_configs: 供应商 HTTP 协议配置
CREATE TABLE IF NOT EXISTS vendor_configs (
    id          SERIAL PRIMARY KEY,
    vendor_id   VARCHAR(64) NOT NULL UNIQUE,
    target_url  VARCHAR(512) NOT NULL,
    http_method VARCHAR(10) NOT NULL DEFAULT 'POST',
    timeout_ms  INT NOT NULL DEFAULT 10000,
    created_at  TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMP NOT NULL DEFAULT NOW()
);

-- notifications: 通知任务表
CREATE TABLE IF NOT EXISTS notifications (
    id               BIGSERIAL PRIMARY KEY,
    notification_id  UUID NOT NULL UNIQUE,
    vendor_id        VARCHAR(64) NOT NULL REFERENCES vendor_configs(vendor_id),
    idempotency_key  VARCHAR(128) UNIQUE,
    headers          JSONB NOT NULL DEFAULT '{}'::jsonb,
    payload          JSONB NOT NULL DEFAULT '{}'::jsonb,
    status           VARCHAR(20) NOT NULL DEFAULT 'pending',
    retry_count      INT NOT NULL DEFAULT 0,
    next_retry_at    TIMESTAMP,
    response_status  INT,
    response_body    TEXT,
    created_at       TIMESTAMP NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMP NOT NULL DEFAULT NOW()
);

-- 状态索引：用于 Dispatcher 轮询查询
CREATE INDEX IF NOT EXISTS idx_notifications_status ON notifications (status);
CREATE INDEX IF NOT EXISTS idx_notifications_next_retry ON notifications (status, next_retry_at);
CREATE INDEX IF NOT EXISTS idx_notifications_in_flight_timeout ON notifications (status, updated_at);
CREATE INDEX IF NOT EXISTS idx_notifications_created_at ON notifications (created_at);
CREATE INDEX IF NOT EXISTS idx_notifications_vendor_id ON notifications (vendor_id);

-- 插入测试用供应商配置
INSERT INTO vendor_configs (vendor_id, target_url, http_method, timeout_ms) VALUES
    ('ad_system', 'https://httpbin.org/post', 'POST', 10000),
    ('crm_system', 'https://httpbin.org/post', 'POST', 5000),
    ('inventory', 'https://httpbin.org/post', 'POST', 8000)
ON CONFLICT (vendor_id) DO NOTHING;
