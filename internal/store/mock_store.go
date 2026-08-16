package store

import (
	"context"
	"fmt"
	"sync"
	"time"

	"rc_sunandsky1992/internal/model"
)

// MockStore 内存实现的 Store，用于单元测试
type MockStore struct {
	mu             sync.Mutex
	Notifications  map[string]*model.Notification
	VendorConfigs  map[string]*model.VendorConfig
	ClaimTaskCalls int
}

func NewMockStore() *MockStore {
	return &MockStore{
		Notifications: make(map[string]*model.Notification),
		VendorConfigs: map[string]*model.VendorConfig{
			"ad_system": {
				VendorID:   "ad_system",
				TargetURL:  "https://httpbin.org/post",
				HTTPMethod: "POST",
				TimeoutMS:  10000,
			},
			"crm_system": {
				VendorID:   "crm_system",
				TargetURL:  "https://httpbin.org/post",
				HTTPMethod: "POST",
				TimeoutMS:  5000,
			},
			"inventory": {
				VendorID:   "inventory",
				TargetURL:  "https://httpbin.org/post",
				HTTPMethod: "POST",
				TimeoutMS:  8000,
			},
		},
	}
}

func (m *MockStore) CreateNotification(ctx context.Context, n *model.Notification) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if existing, ok := m.Notifications[n.NotificationID]; ok {
		_ = existing
		return fmt.Errorf("notification already exists")
	}
	// 模拟 idempotency_key 唯一约束（与 PG 的 UNIQUE 索引一致）
	if n.IdempotencyKey != "" {
		for _, item := range m.Notifications {
			if item.IdempotencyKey == n.IdempotencyKey {
				return fmt.Errorf("notification already exists")
			}
		}
	}
	m.Notifications[n.NotificationID] = n
	return nil
}

func (m *MockStore) GetNotification(ctx context.Context, notificationID string) (*model.Notification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.Notifications[notificationID]
	if !ok {
		return nil, fmt.Errorf("not found")
	}
	return n, nil
}

func (m *MockStore) GetNotificationByIdempotencyKey(ctx context.Context, key string) (*model.Notification, error) {
	if key == "" {
		return nil, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, n := range m.Notifications {
		if n.IdempotencyKey == key {
			return n, nil
		}
	}
	return nil, nil
}

func (m *MockStore) ClaimTask(ctx context.Context) (*model.Notification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ClaimTaskCalls++
	now := time.Now()
	for _, n := range m.Notifications {
		if n.Status == model.StatusPending {
			n.Status = model.StatusInFlight
			n.UpdatedAt = now
			return n, nil
		}
		if n.Status == model.StatusRetrying && n.NextRetryAt != nil && n.NextRetryAt.Before(now) {
			n.Status = model.StatusInFlight
			n.UpdatedAt = now
			return n, nil
		}
		if n.Status == model.StatusInFlight && n.UpdatedAt.Before(now.Add(-60*time.Second)) {
			n.UpdatedAt = now
			return n, nil
		}
	}
	return nil, nil
}

func (m *MockStore) UpdateStatus(ctx context.Context, notificationID string, status model.NotificationStatus, retryCount int, nextRetryAt *time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.Notifications[notificationID]
	if !ok {
		return fmt.Errorf("not found")
	}
	n.Status = status
	n.RetryCount = retryCount
	n.NextRetryAt = nextRetryAt
	n.UpdatedAt = time.Now()
	return nil
}

func (m *MockStore) UpdateResponse(ctx context.Context, notificationID string, statusCode int, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.Notifications[notificationID]
	if !ok {
		return fmt.Errorf("not found")
	}
	n.ResponseStatus = statusCode
	n.ResponseBody = body
	return nil
}

func (m *MockStore) GetVendorConfig(ctx context.Context, vendorID string) (*model.VendorConfig, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.VendorConfigs[vendorID]
	if !ok {
		return nil, fmt.Errorf("vendor not found")
	}
	return c, nil
}

func (m *MockStore) GetDeadLetters(ctx context.Context) ([]*model.Notification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*model.Notification
	for _, n := range m.Notifications {
		if n.Status == model.StatusDead {
			result = append(result, n)
		}
	}
	return result, nil
}

func (m *MockStore) RetryDeadLetter(ctx context.Context, notificationID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	n, ok := m.Notifications[notificationID]
	if !ok {
		return fmt.Errorf("not found or not in dead status")
	}
	if n.Status != model.StatusDead {
		return fmt.Errorf("not found or not in dead status")
	}
	n.Status = model.StatusPending
	n.RetryCount = 0
	n.NextRetryAt = nil
	n.UpdatedAt = time.Now()
	return nil
}

func (m *MockStore) GetStatsOverview(ctx context.Context, start, end time.Time) (*model.StatsOverview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := &model.StatsOverview{}
	for _, n := range m.Notifications {
		if n.CreatedAt.Before(start) || n.CreatedAt.After(end) {
			continue
		}
		s.Total++
		switch n.Status {
		case model.StatusDelivered:
			s.Delivered++
		case model.StatusDead:
			s.Dead++
		case model.StatusPending:
			s.Pending++
		case model.StatusInFlight:
			s.InFlight++
		case model.StatusRetrying:
			s.Retrying++
		}
	}
	s.Failed = int64(s.Total - s.Delivered - s.Pending - s.InFlight - s.Retrying + s.Dead - s.Dead)
	if s.Total > 0 {
		s.SuccessRate = float64(s.Delivered) / float64(s.Total)
		s.FailureRate = float64(s.Dead) / float64(s.Total)
	}
	return s, nil
}

func (m *MockStore) GetStatsByVendor(ctx context.Context, start, end time.Time) ([]*model.StatsByVendor, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	vendorMap := make(map[string]*model.StatsByVendor)
	for _, n := range m.Notifications {
		if n.CreatedAt.Before(start) || n.CreatedAt.After(end) {
			continue
		}
		s, ok := vendorMap[n.VendorID]
		if !ok {
			s = &model.StatsByVendor{VendorID: n.VendorID}
			vendorMap[n.VendorID] = s
		}
		s.Total++
		if n.Status == model.StatusDelivered {
			s.Delivered++
		}
		if n.Status == model.StatusDead {
			s.Dead++
		}
	}
	var result []*model.StatsByVendor
	for _, s := range vendorMap {
		if s.Total > 0 {
			s.SuccessRate = float64(s.Delivered) / float64(s.Total)
		}
		result = append(result, s)
	}
	return result, nil
}

func (m *MockStore) GetRetryDistribution(ctx context.Context, start, end time.Time) ([]*model.RetryDistribution, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	distMap := make(map[int]*model.RetryDistribution)
	for _, n := range m.Notifications {
		if n.CreatedAt.Before(start) || n.CreatedAt.After(end) {
			continue
		}
		d, ok := distMap[n.RetryCount]
		if !ok {
			d = &model.RetryDistribution{RetryCount: n.RetryCount}
			distMap[n.RetryCount] = d
		}
		d.Total++
	}
	var result []*model.RetryDistribution
	for _, d := range distMap {
		result = append(result, d)
	}
	return result, nil
}
