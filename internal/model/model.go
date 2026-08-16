package model

import "time"

// NotificationStatus 通知任务状态
type NotificationStatus string

const (
	StatusPending   NotificationStatus = "pending"
	StatusInFlight  NotificationStatus = "in_flight"
	StatusRetrying  NotificationStatus = "retrying"
	StatusDelivered NotificationStatus = "delivered"
	StatusDead      NotificationStatus = "dead"
)

// MaxRetries 最大重试次数
const MaxRetries = 8

// Notification 通知任务
type Notification struct {
	ID             int64              `json:"-"`
	NotificationID string             `json:"notification_id"`
	VendorID       string             `json:"vendor_id"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
	Headers        map[string]string  `json:"headers"`
	Payload        interface{}        `json:"payload"`
	Status         NotificationStatus `json:"status"`
	RetryCount     int                `json:"retry_count"`
	NextRetryAt    *time.Time         `json:"next_retry_at,omitempty"`
	ResponseStatus int                `json:"response_status,omitempty"`
	ResponseBody   string             `json:"response_body,omitempty"`
	CreatedAt      time.Time          `json:"created_at"`
	UpdatedAt      time.Time          `json:"updated_at"`
}

// VendorConfig 供应商配置
type VendorConfig struct {
	ID         int64     `json:"-"`
	VendorID   string    `json:"vendor_id"`
	TargetURL  string    `json:"target_url"`
	HTTPMethod string    `json:"http_method"`
	TimeoutMS  int       `json:"timeout_ms"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// CreateNotificationRequest 提交通知请求
type CreateNotificationRequest struct {
	VendorID       string                 `json:"vendor_id" binding:"required"`
	IdempotencyKey string                 `json:"idempotency_key"`
	Headers        map[string]string      `json:"headers"`
	Payload        map[string]interface{} `json:"payload" binding:"required"`
}

// CreateNotificationResponse 提交通知响应
type CreateNotificationResponse struct {
	NotificationID string             `json:"notification_id"`
	Status         NotificationStatus `json:"status"`
}

// TimeRange 统计时间范围
type TimeRange struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
}

// StatsOverview 统计概览
type StatsOverview struct {
	TimeRange   *TimeRange `json:"time_range"`
	Total       int64      `json:"total"`
	Delivered   int64      `json:"delivered"`
	Failed      int64      `json:"failed"`
	Dead        int64      `json:"dead"`
	Pending     int64      `json:"pending"`
	InFlight    int64      `json:"in_flight"`
	Retrying    int64      `json:"retrying"`
	SuccessRate float64    `json:"success_rate"`
	FailureRate float64    `json:"failure_rate"`
}

// StatsByVendor 按供应商统计
type StatsByVendor struct {
	VendorID    string  `json:"vendor_id"`
	Total       int64   `json:"total"`
	Delivered   int64   `json:"delivered"`
	Dead        int64   `json:"dead"`
	SuccessRate float64 `json:"success_rate"`
}

// RetryDistribution 重试分布
type RetryDistribution struct {
	RetryCount int   `json:"retry_count"`
	Total      int64 `json:"total"`
}
