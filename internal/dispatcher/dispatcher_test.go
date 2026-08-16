package dispatcher

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"rc_sunandsky1992/internal/model"
	"rc_sunandsky1992/internal/store"
)

// --- Mock HTTP Client ---

type MockHTTPClient struct {
	StatusCode int
	Body       string
	Err        error
	CallCount  int
	LastReq    *http.Request
}

func (m *MockHTTPClient) Do(req *http.Request) (*http.Response, error) {
	m.CallCount++
	m.LastReq = req
	if m.Err != nil {
		return nil, m.Err
	}
	return &http.Response{
		StatusCode: m.StatusCode,
		Body:       io.NopCloser(strings.NewReader(m.Body)),
		Header:     make(http.Header),
	}, nil
}

// --- Test: 2xx → delivered ---

func TestProcessTask_Success(t *testing.T) {
	ms := store.NewMockStore()
	mc := &MockHTTPClient{StatusCode: 200, Body: `{"ok":true}`}

	// 插入一个 pending 任务
	n := &model.Notification{
		NotificationID: "test-001",
		VendorID:       "ad_system",
		Headers:        map[string]string{"Authorization": "Bearer xxx"},
		Payload:        map[string]interface{}{"user_id": "12345"},
		Status:         model.StatusPending,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	ms.Notifications["test-001"] = n

	d := New(ms, mc)
	err := d.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	result := ms.Notifications["test-001"]
	if result.Status != model.StatusDelivered {
		t.Errorf("expected delivered, got %s", result.Status)
	}
	if result.ResponseStatus != 200 {
		t.Errorf("expected response status 200, got %d", result.ResponseStatus)
	}
}

// --- Test: 5xx → retrying ---

func TestProcessTask_5xx_Retrying(t *testing.T) {
	ms := store.NewMockStore()
	mc := &MockHTTPClient{StatusCode: 500, Body: `{"error":"internal"}`}

	n := &model.Notification{
		NotificationID: "test-002",
		VendorID:       "ad_system",
		Headers:        map[string]string{},
		Payload:        map[string]interface{}{"event": "register"},
		Status:         model.StatusPending,
		RetryCount:     0,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	ms.Notifications["test-002"] = n

	d := New(ms, mc)
	err := d.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	result := ms.Notifications["test-002"]
	if result.Status != model.StatusRetrying {
		t.Errorf("expected retrying, got %s", result.Status)
	}
	if result.RetryCount != 1 {
		t.Errorf("expected retry_count 1, got %d", result.RetryCount)
	}
	if result.NextRetryAt == nil {
		t.Error("expected next_retry_at to be set")
	}
}

// --- Test: 4xx → dead ---

func TestProcessTask_4xx_Dead(t *testing.T) {
	ms := store.NewMockStore()
	mc := &MockHTTPClient{StatusCode: 401, Body: `{"error":"unauthorized"}`}

	n := &model.Notification{
		NotificationID: "test-003",
		VendorID:       "ad_system",
		Headers:        map[string]string{},
		Payload:        map[string]interface{}{"event": "register"},
		Status:         model.StatusPending,
		RetryCount:     0,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	ms.Notifications["test-003"] = n

	d := New(ms, mc)
	err := d.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	result := ms.Notifications["test-003"]
	if result.Status != model.StatusDead {
		t.Errorf("expected dead, got %s", result.Status)
	}
}

// --- Test: network error → retrying ---

func TestProcessTask_NetworkError_Retrying(t *testing.T) {
	ms := store.NewMockStore()
	mc := &MockHTTPClient{Err: errors.New("connection refused")}

	n := &model.Notification{
		NotificationID: "test-004",
		VendorID:       "ad_system",
		Headers:        map[string]string{},
		Payload:        map[string]interface{}{"event": "register"},
		Status:         model.StatusPending,
		RetryCount:     0,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	ms.Notifications["test-004"] = n

	d := New(ms, mc)
	err := d.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	result := ms.Notifications["test-004"]
	if result.Status != model.StatusRetrying {
		t.Errorf("expected retrying, got %s", result.Status)
	}
	if result.ResponseStatus != 0 {
		t.Errorf("expected response status 0, got %d", result.ResponseStatus)
	}
}

// --- Test: max retries → dead ---

func TestProcessTask_MaxRetries_Dead(t *testing.T) {
	ms := store.NewMockStore()
	mc := &MockHTTPClient{StatusCode: 500, Body: `{"error":"internal"}`}

	n := &model.Notification{
		NotificationID: "test-005",
		VendorID:       "ad_system",
		Headers:        map[string]string{},
		Payload:        map[string]interface{}{"event": "register"},
		Status:         model.StatusRetrying,
		RetryCount:     model.MaxRetries, // 已达上限
		NextRetryAt:    &time.Time{},     // 已到期
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now().Add(-2 * time.Minute),
	}
	ms.Notifications["test-005"] = n

	d := New(ms, mc)
	err := d.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	result := ms.Notifications["test-005"]
	if result.Status != model.StatusDead {
		t.Errorf("expected dead, got %s", result.Status)
	}
}

// --- Test: no task available ---

func TestProcessTask_NoTask(t *testing.T) {
	ms := store.NewMockStore()
	mc := &MockHTTPClient{StatusCode: 200, Body: ""}

	d := New(ms, mc)
	err := d.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if mc.CallCount != 0 {
		t.Errorf("expected 0 HTTP calls, got %d", mc.CallCount)
	}
}

// --- Test: backoff calculation ---

func TestBackoffCalculation(t *testing.T) {
	d := &Dispatcher{}

	cases := []struct {
		retryCount int
		expected   time.Duration
	}{
		{1, 1 * time.Second},
		{2, 2 * time.Second},
		{3, 4 * time.Second},
		{4, 8 * time.Second},
		{5, 16 * time.Second},
		{6, 32 * time.Second},
		{7, 64 * time.Second},
		{8, 128 * time.Second},
	}

	for _, c := range cases {
		got := d.backoff(c.retryCount)
		if got != c.expected {
			t.Errorf("retry %d: expected %v, got %v", c.retryCount, c.expected, got)
		}
	}
}

// --- Test: HTTP request built correctly ---

func TestBuildHTTPRequest(t *testing.T) {
	ms := store.NewMockStore()
	mc := &MockHTTPClient{StatusCode: 200, Body: ""}

	n := &model.Notification{
		NotificationID: "test-006",
		VendorID:       "ad_system",
		Headers:        map[string]string{"Authorization": "Bearer xxx", "X-Custom": "abc"},
		Payload:        map[string]interface{}{"user_id": "12345", "event": "register"},
		Status:         model.StatusPending,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	ms.Notifications["test-006"] = n

	d := New(ms, mc)
	err := d.ProcessOne(context.Background())
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}

	req := mc.LastReq
	if req == nil {
		t.Fatal("expected HTTP request to be made")
	}
	if req.URL.String() != "https://httpbin.org/post" {
		t.Errorf("expected URL https://httpbin.org/post, got %s", req.URL.String())
	}
	if req.Method != "POST" {
		t.Errorf("expected POST, got %s", req.Method)
	}
	if req.Header.Get("Authorization") != "Bearer xxx" {
		t.Errorf("expected Authorization header, got %s", req.Header.Get("Authorization"))
	}
	if req.Header.Get("X-Custom") != "abc" {
		t.Errorf("expected X-Custom header, got %s", req.Header.Get("X-Custom"))
	}

	// 验证 body
	var body map[string]interface{}
	if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode body: %v", err)
	}
	if body["user_id"] != "12345" {
		t.Errorf("expected user_id 12345, got %v", body["user_id"])
	}
	if body["event"] != "register" {
		t.Errorf("expected event register, got %v", body["event"])
	}
}

// --- Test: response body truncated ---

func TestResponseBodyTruncated(t *testing.T) {
	ms := store.NewMockStore()
	longBody := make([]byte, 8192)
	for i := range longBody {
		longBody[i] = 'a'
	}
	mc := &MockHTTPClient{StatusCode: 500, Body: string(longBody)}

	n := &model.Notification{
		NotificationID: "test-007",
		VendorID:       "ad_system",
		Headers:        map[string]string{},
		Payload:        map[string]interface{}{"event": "register"},
		Status:         model.StatusPending,
		RetryCount:     0,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	ms.Notifications["test-007"] = n

	d := New(ms, mc)
	_ = d.ProcessOne(context.Background())

	result := ms.Notifications["test-007"]
	if len(result.ResponseBody) > 4096 {
		t.Errorf("expected response body <= 4096 bytes, got %d", len(result.ResponseBody))
	}
}

// --- Test: vendor timeout_ms applied to request context ---

func TestVendorTimeoutApplied(t *testing.T) {
	ms := store.NewMockStore()
	mc := &MockHTTPClient{StatusCode: 200, Body: ""}

	n := &model.Notification{
		NotificationID: "test-008",
		VendorID:       "ad_system", // ad_system TimeoutMS = 10000
		Headers:        map[string]string{},
		Payload:        map[string]interface{}{"event": "register"},
		Status:         model.StatusPending,
		CreatedAt:      time.Now(),
		UpdatedAt:      time.Now(),
	}
	ms.Notifications["test-008"] = n

	d := New(ms, mc)
	_ = d.ProcessOne(context.Background())

	req := mc.LastReq
	if req == nil {
		t.Fatal("expected HTTP request to be made")
	}
	deadline, ok := req.Context().Deadline()
	if !ok {
		t.Fatal("expected request context to have a deadline from vendor timeout")
	}
	expected := time.Now().Add(10000 * time.Millisecond)
	diff := deadline.Sub(expected)
	if diff > 200*time.Millisecond || diff < -200*time.Millisecond {
		t.Errorf("expected deadline ~10s from now, got %v", deadline)
	}
}
