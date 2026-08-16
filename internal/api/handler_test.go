package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"rc_sunandsky1992/internal/model"
	"rc_sunandsky1992/internal/store"

	"github.com/gin-gonic/gin"
)

func setupRouter(s store.Store) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	h := NewHandler(s)
	r.POST("/api/notifications", h.CreateNotification)
	r.GET("/api/notifications/:id", h.GetNotification)
	r.GET("/api/dead-letters", h.GetDeadLetters)
	r.POST("/api/dead-letters/:id/retry", h.RetryDeadLetter)
	r.GET("/api/stats", h.GetStats)
	r.GET("/api/stats/by-vendor", h.GetStatsByVendor)
	r.GET("/api/stats/retry-distribution", h.GetRetryDistribution)
	return r
}

// --- Test: POST /api/notifications success ---

func TestCreateNotification_Success(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	body := `{"vendor_id":"ad_system","idempotency_key":"key-001","headers":{"Authorization":"Bearer xxx"},"payload":{"user_id":"12345","event":"register"}}`
	req := httptest.NewRequest("POST", "/api/notifications", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("expected 202, got %d", w.Code)
	}

	var resp model.CreateNotificationResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.NotificationID == "" {
		t.Error("expected notification_id to be set")
	}
	if resp.Status != model.StatusPending {
		t.Errorf("expected pending, got %s", resp.Status)
	}
}

// --- Test: POST /api/notifications invalid vendor ---

func TestCreateNotification_InvalidVendor(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	body := `{"vendor_id":"nonexistent","payload":{"event":"register"}}`
	req := httptest.NewRequest("POST", "/api/notifications", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- Test: POST /api/notifications empty payload ---

func TestCreateNotification_EmptyPayload(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	body := `{"vendor_id":"ad_system"}`
	req := httptest.NewRequest("POST", "/api/notifications", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	r.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

// --- Test: POST /api/notifications idempotency duplicate ---

func TestCreateNotification_IdempotencyDuplicate(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	// 先提交一次
	body := `{"vendor_id":"ad_system","idempotency_key":"dup-key","payload":{"event":"register"}}`
	req1 := httptest.NewRequest("POST", "/api/notifications", bytes.NewBufferString(body))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	r.ServeHTTP(w1, req1)

	if w1.Code != http.StatusAccepted {
		t.Fatalf("first request expected 202, got %d", w1.Code)
	}

	var resp1 model.CreateNotificationResponse
	json.Unmarshal(w1.Body.Bytes(), &resp1)

	// 重复提交
	req2 := httptest.NewRequest("POST", "/api/notifications", bytes.NewBufferString(body))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req2)

	if w2.Code != http.StatusAccepted {
		t.Errorf("duplicate request expected 202, got %d", w2.Code)
	}

	var resp2 model.CreateNotificationResponse
	json.Unmarshal(w2.Body.Bytes(), &resp2)
	if resp1.NotificationID != resp2.NotificationID {
		t.Error("expected same notification_id for duplicate idempotency key")
	}
}

// --- Test: GET /api/notifications/:id found ---

func TestGetNotification_Found(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	// 插入测试数据
	n := &model.Notification{
		NotificationID: "get-test-001",
		VendorID:       "ad_system",
		Status:         model.StatusDelivered,
		RetryCount:     0,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	s.Notifications["get-test-001"] = n

	req := httptest.NewRequest("GET", "/api/notifications/get-test-001", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp model.Notification
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.NotificationID != "get-test-001" {
		t.Error("expected correct notification_id")
	}
}

// --- Test: GET /api/notifications/:id not found ---

func TestGetNotification_NotFound(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	req := httptest.NewRequest("GET", "/api/notifications/nonexistent", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- Test: GET /api/dead-letters ---

func TestGetDeadLetters(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	// 插入一个死信
	n := &model.Notification{
		NotificationID: "dead-001",
		VendorID:       "ad_system",
		Status:         model.StatusDead,
		RetryCount:     8,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	s.Notifications["dead-001"] = n

	req := httptest.NewRequest("GET", "/api/dead-letters", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp []*model.Notification
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp) != 1 {
		t.Errorf("expected 1 dead letter, got %d", len(resp))
	}
}

// --- Test: POST /api/dead-letters/:id/retry ---

func TestRetryDeadLetter(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	n := &model.Notification{
		NotificationID: "dead-002",
		VendorID:       "ad_system",
		Status:         model.StatusDead,
		RetryCount:     8,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	s.Notifications["dead-002"] = n

	req := httptest.NewRequest("POST", "/api/dead-letters/dead-002/retry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	result := s.Notifications["dead-002"]
	if result.Status != model.StatusPending {
		t.Errorf("expected pending, got %s", result.Status)
	}
	if result.RetryCount != 0 {
		t.Errorf("expected retry_count 0, got %d", result.RetryCount)
	}
}

// --- Test: GET /api/stats ---

func TestGetStats(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	// 插入测试数据
	now := time.Now()
	s.Notifications["s1"] = &model.Notification{NotificationID: "s1", VendorID: "ad_system", Status: model.StatusDelivered, CreatedAt: now, UpdatedAt: now}
	s.Notifications["s2"] = &model.Notification{NotificationID: "s2", VendorID: "ad_system", Status: model.StatusDelivered, CreatedAt: now, UpdatedAt: now}
	s.Notifications["s3"] = &model.Notification{NotificationID: "s3", VendorID: "ad_system", Status: model.StatusDead, CreatedAt: now, UpdatedAt: now}

	start := now.Add(-1 * time.Hour).Format(time.RFC3339)
	end := now.Add(1 * time.Hour).Format(time.RFC3339)
	req := httptest.NewRequest("GET", "/api/stats?start="+start+"&end="+end, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp model.StatsOverview
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 3 {
		t.Errorf("expected total 3, got %d", resp.Total)
	}
	if resp.Delivered != 2 {
		t.Errorf("expected delivered 2, got %d", resp.Delivered)
	}
}

// --- Test: GET /api/stats/by-vendor ---

func TestGetStatsByVendor(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	now := time.Now()
	s.Notifications["v1"] = &model.Notification{NotificationID: "v1", VendorID: "ad_system", Status: model.StatusDelivered, CreatedAt: now, UpdatedAt: now}
	s.Notifications["v2"] = &model.Notification{NotificationID: "v2", VendorID: "crm_system", Status: model.StatusDead, CreatedAt: now, UpdatedAt: now}

	start := now.Add(-1 * time.Hour).Format(time.RFC3339)
	end := now.Add(1 * time.Hour).Format(time.RFC3339)
	req := httptest.NewRequest("GET", "/api/stats/by-vendor?start="+start+"&end="+end, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp []*model.StatsByVendor
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp) != 2 {
		t.Errorf("expected 2 vendors, got %d", len(resp))
	}
}

// --- Test: GET /api/stats/retry-distribution ---

func TestGetRetryDistribution(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	now := time.Now()
	s.Notifications["r1"] = &model.Notification{NotificationID: "r1", VendorID: "ad_system", Status: model.StatusDelivered, RetryCount: 0, CreatedAt: now, UpdatedAt: now}
	s.Notifications["r2"] = &model.Notification{NotificationID: "r2", VendorID: "ad_system", Status: model.StatusDelivered, RetryCount: 1, CreatedAt: now, UpdatedAt: now}
	s.Notifications["r3"] = &model.Notification{NotificationID: "r3", VendorID: "ad_system", Status: model.StatusDead, RetryCount: 8, CreatedAt: now, UpdatedAt: now}

	start := now.Add(-1 * time.Hour).Format(time.RFC3339)
	end := now.Add(1 * time.Hour).Format(time.RFC3339)
	req := httptest.NewRequest("GET", "/api/stats/retry-distribution?start="+start+"&end="+end, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var resp []*model.RetryDistribution
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp) != 3 {
		t.Errorf("expected 3 retry buckets, got %d", len(resp))
	}
}

// --- Test: POST /api/dead-letters/:id/retry not found (不 panic) ---

func TestRetryDeadLetter_NotFound(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	req := httptest.NewRequest("POST", "/api/dead-letters/nonexistent/retry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- Test: POST /api/dead-letters/:id/retry 非 dead 状态 (不 panic) ---

func TestRetryDeadLetter_NotDeadStatus(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	s.Notifications["alive-001"] = &model.Notification{
		NotificationID: "alive-001",
		VendorID:       "ad_system",
		Status:         model.StatusDelivered,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}

	req := httptest.NewRequest("POST", "/api/dead-letters/alive-001/retry", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", w.Code)
	}
}

// --- Test: 并发幂等（相同 idempotency_key 同时提交） ---

func TestCreateNotification_IdempotencyConcurrent(t *testing.T) {
	s := store.NewMockStore()
	r := setupRouter(s)

	body := `{"vendor_id":"ad_system","idempotency_key":"concurrent-key","payload":{"event":"register"}}`

	const n = 20
	var wg sync.WaitGroup
	codes := make([]int, n)
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/api/notifications", bytes.NewBufferString(body))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			codes[idx] = w.Code
			var resp model.CreateNotificationResponse
			json.Unmarshal(w.Body.Bytes(), &resp)
			ids[idx] = resp.NotificationID
		}(i)
	}
	wg.Wait()

	for i, code := range codes {
		if code != http.StatusAccepted {
			t.Errorf("request %d expected 202, got %d", i, code)
		}
	}

	// 所有响应必须是同一个 notification_id
	for i := 1; i < n; i++ {
		if ids[i] == "" || ids[i] != ids[0] {
			t.Errorf("request %d expected same notification_id %s, got %s", i, ids[0], ids[i])
		}
	}

	// 只创建了一条记录
	count := 0
	for _, item := range s.Notifications {
		if item.IdempotencyKey == "concurrent-key" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 notification created, got %d", count)
	}
}
