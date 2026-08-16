package store

import (
	"context"
	"time"

	"rc_sunandsky1992/internal/model"
)

// Store 定义数据访问层接口，方便上层 mock 测试
type Store interface {
	// CreateNotification 创建通知任务
	CreateNotification(ctx context.Context, n *model.Notification) error

	// GetNotification 按 notification_id 查询
	GetNotification(ctx context.Context, notificationID string) (*model.Notification, error)

	// GetNotificationByIdempotencyKey 按幂等键查询
	GetNotificationByIdempotencyKey(ctx context.Context, key string) (*model.Notification, error)

	// ClaimTask 抢占一个待投递任务（pending / 到期 retrying / 超时 in_flight）
	ClaimTask(ctx context.Context) (*model.Notification, error)

	// UpdateStatus 更新任务状态
	UpdateStatus(ctx context.Context, notificationID string, status model.NotificationStatus, retryCount int, nextRetryAt *time.Time) error

	// UpdateResponse 更新回执
	UpdateResponse(ctx context.Context, notificationID string, statusCode int, body string) error

	// GetVendorConfig 查供应商配置
	GetVendorConfig(ctx context.Context, vendorID string) (*model.VendorConfig, error)

	// GetDeadLetters 查死信列表
	GetDeadLetters(ctx context.Context) ([]*model.Notification, error)

	// RetryDeadLetter 重投死信
	RetryDeadLetter(ctx context.Context, notificationID string) error

	// GetStatsOverview 统计概览
	GetStatsOverview(ctx context.Context, start, end time.Time) (*model.StatsOverview, error)

	// GetStatsByVendor 按供应商统计
	GetStatsByVendor(ctx context.Context, start, end time.Time) ([]*model.StatsByVendor, error)

	// GetRetryDistribution 重试分布
	GetRetryDistribution(ctx context.Context, start, end time.Time) ([]*model.RetryDistribution, error)
}
