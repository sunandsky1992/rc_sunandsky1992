package api

import (
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"rc_sunandsky1992/internal/model"
	"rc_sunandsky1992/internal/store"
)

// Handler HTTP 接口处理器
type Handler struct {
	store store.Store
}

// NewHandler 创建 Handler
func NewHandler(s store.Store) *Handler {
	return &Handler{store: s}
}

// CreateNotification POST /api/notifications
func (h *Handler) CreateNotification(c *gin.Context) {
	var req model.CreateNotificationRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	// 校验 vendor_id 是否存在
	ctx := c.Request.Context()
	if _, err := h.store.GetVendorConfig(ctx, req.VendorID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "vendor_id not found"})
		return
	}

	// 幂等去重
	if req.IdempotencyKey != "" {
		existing, _ := h.store.GetNotificationByIdempotencyKey(ctx, req.IdempotencyKey)
		if existing != nil {
			log.Printf("idempotency hit: key=%s notification_id=%s", req.IdempotencyKey, existing.NotificationID)
			c.JSON(http.StatusAccepted, model.CreateNotificationResponse{
				NotificationID: existing.NotificationID,
				Status:         existing.Status,
			})
			return
		}
	}

	// 生成 notification_id
	notificationID := uuid.New().String()

	// headers 为空时兜底为空对象，与 DB 默认值 '{}' 保持一致
	headers := req.Headers
	if headers == nil {
		headers = map[string]string{}
	}

	now := time.Now()
	n := &model.Notification{
		NotificationID: notificationID,
		VendorID:       req.VendorID,
		IdempotencyKey: req.IdempotencyKey,
		Headers:        headers,
		Payload:        req.Payload,
		Status:         model.StatusPending,
		RetryCount:     0,
		CreatedAt:      now,
		UpdatedAt:      now,
	}

	if err := h.store.CreateNotification(ctx, n); err != nil {
		// 并发幂等冲突：唯一约束冲突，返回已有结果
		if req.IdempotencyKey != "" {
			existing, getErr := h.store.GetNotificationByIdempotencyKey(ctx, req.IdempotencyKey)
			if getErr == nil && existing != nil {
				log.Printf("idempotency conflict resolved: key=%s notification_id=%s", req.IdempotencyKey, existing.NotificationID)
				c.JSON(http.StatusAccepted, model.CreateNotificationResponse{
					NotificationID: existing.NotificationID,
					Status:         existing.Status,
				})
				return
			}
		}
		log.Printf("create notification error: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.JSON(http.StatusAccepted, model.CreateNotificationResponse{
		NotificationID: notificationID,
		Status:         model.StatusPending,
	})
	log.Printf("notification created: id=%s vendor=%s idempotency_key=%s", notificationID, req.VendorID, req.IdempotencyKey)
}

// GetNotification GET /api/notifications/:id
func (h *Handler) GetNotification(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()

	n, err := h.store.GetNotification(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	c.JSON(http.StatusOK, n)
}

// GetDeadLetters GET /api/dead-letters
func (h *Handler) GetDeadLetters(c *gin.Context) {
	ctx := c.Request.Context()

	letters, err := h.store.GetDeadLetters(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.JSON(http.StatusOK, letters)
}

// RetryDeadLetter POST /api/dead-letters/:id/retry
func (h *Handler) RetryDeadLetter(c *gin.Context) {
	id := c.Param("id")
	ctx := c.Request.Context()

	if err := h.store.RetryDeadLetter(ctx, id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}

	log.Printf("dead letter retried: id=%s", id)

	n, err := h.store.GetNotification(ctx, id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"notification_id": n.NotificationID,
		"status":          n.Status,
		"retry_count":     n.RetryCount,
	})
}

// GetStats GET /api/stats?start=...&end=...
func (h *Handler) GetStats(c *gin.Context) {
	ctx := c.Request.Context()
	start, end := parseTimeRange(c)

	stats, err := h.store.GetStatsOverview(ctx, start, end)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	// 回显查询时间范围，方便调用方核对统计口径
	stats.TimeRange = &model.TimeRange{Start: start, End: end}

	c.JSON(http.StatusOK, stats)
}

// GetStatsByVendor GET /api/stats/by-vendor?start=...&end=...
func (h *Handler) GetStatsByVendor(c *gin.Context) {
	ctx := c.Request.Context()
	start, end := parseTimeRange(c)

	stats, err := h.store.GetStatsByVendor(ctx, start, end)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.JSON(http.StatusOK, stats)
}

// GetRetryDistribution GET /api/stats/retry-distribution?start=...&end=...
func (h *Handler) GetRetryDistribution(c *gin.Context) {
	ctx := c.Request.Context()
	start, end := parseTimeRange(c)

	dist, err := h.store.GetRetryDistribution(ctx, start, end)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}

	c.JSON(http.StatusOK, dist)
}

// parseTimeRange 解析时间范围参数
func parseTimeRange(c *gin.Context) (time.Time, time.Time) {
	startStr := c.Query("start")
	endStr := c.Query("end")

	start := time.Now().Add(-24 * time.Hour)
	end := time.Now()

	if startStr != "" {
		if t, err := time.Parse(time.RFC3339, startStr); err == nil {
			start = t
		}
	}
	if endStr != "" {
		if t, err := time.Parse(time.RFC3339, endStr); err == nil {
			end = t
		}
	}

	return start, end
}
