package dispatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"rc_sunandsky1992/internal/model"
	"rc_sunandsky1992/internal/store"
)

// HTTPClient 接口，方便 mock 测试
type HTTPClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Dispatcher 投递引擎
type Dispatcher struct {
	store     store.Store
	client    HTTPClient
	pollDelay time.Duration
}

// New 创建 Dispatcher
func New(s store.Store, client HTTPClient) *Dispatcher {
	return &Dispatcher{
		store:     s,
		client:    client,
		pollDelay: 1 * time.Second,
	}
}

// Run 启动轮询循环
func (d *Dispatcher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
			if err := d.ProcessOne(ctx); err != nil {
				log.Printf("dispatcher error: %v", err)
			}
			// 没有任务时 sleep
			time.Sleep(d.pollDelay)
		}
	}
}

// ProcessOne 处理一个任务
func (d *Dispatcher) ProcessOne(ctx context.Context) error {
	// 1. 抢占任务
	task, err := d.store.ClaimTask(ctx)
	if err != nil {
		return fmt.Errorf("claim task: %w", err)
	}
	if task == nil {
		return nil // 没有任务
	}
	log.Printf("claimed: notification=%s vendor=%s retry=%d", task.NotificationID, task.VendorID, task.RetryCount)

	// 2. 查供应商配置
	vendor, err := d.store.GetVendorConfig(ctx, task.VendorID)
	if err != nil {
		log.Printf("get vendor config failed: notification=%s vendor=%s err=%v", task.NotificationID, task.VendorID, err)
		return fmt.Errorf("get vendor config: %w", err)
	}

	// 3. 构建 HTTP 请求
	req, err := d.buildRequest(task, vendor)
	if err != nil {
		log.Printf("build request failed: notification=%s err=%v", task.NotificationID, err)
		return fmt.Errorf("build request: %w", err)
	}

	// 4. 执行投递（超时按供应商配置，默认 30s）
	timeout := 30 * time.Second
	if vendor.TimeoutMS > 0 {
		timeout = time.Duration(vendor.TimeoutMS) * time.Millisecond
	}
	deliverCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req = req.WithContext(deliverCtx)

	startTime := time.Now()
	resp, err := d.client.Do(req)
	if err != nil {
		// 网络错误 → 重试
		log.Printf("deliver failed (network): notification=%s err=%v duration=%v", task.NotificationID, err, time.Since(startTime))
		return d.handleRetry(ctx, task, 0, "")
	}
	defer resp.Body.Close()

	// 读取响应体（截断到 4KB）
	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	bodyStr := string(bodyBytes)

	// 记录回执
	_ = d.store.UpdateResponse(ctx, task.NotificationID, resp.StatusCode, bodyStr)

	// 5. 处理结果
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// 成功
		log.Printf("delivered: notification=%s status=%d duration=%v", task.NotificationID, resp.StatusCode, time.Since(startTime))
		return d.store.UpdateStatus(ctx, task.NotificationID, model.StatusDelivered, task.RetryCount, nil)
	}

	if resp.StatusCode >= 400 && resp.StatusCode < 500 {
		// 4xx → dead，重试无意义
		log.Printf("deliver 4xx dead: notification=%s status=%d", task.NotificationID, resp.StatusCode)
		return d.store.UpdateStatus(ctx, task.NotificationID, model.StatusDead, task.RetryCount, nil)
	}

	// 5xx → 重试
	return d.handleRetry(ctx, task, resp.StatusCode, bodyStr)
}

// handleRetry 处理重试逻辑
func (d *Dispatcher) handleRetry(ctx context.Context, task *model.Notification, statusCode int, body string) error {
	retryCount := task.RetryCount + 1

	if statusCode > 0 {
		_ = d.store.UpdateResponse(ctx, task.NotificationID, statusCode, body)
	}

	if retryCount > model.MaxRetries {
		log.Printf("max retries reached, dead: notification=%s retries=%d", task.NotificationID, retryCount)
		return d.store.UpdateStatus(ctx, task.NotificationID, model.StatusDead, retryCount, nil)
	}

	nextRetryAt := time.Now().Add(d.backoff(retryCount))
	log.Printf("retrying: notification=%s retry=%d next_at=%v", task.NotificationID, retryCount, nextRetryAt)
	return d.store.UpdateStatus(ctx, task.NotificationID, model.StatusRetrying, retryCount, &nextRetryAt)
}

// buildRequest 构建 HTTP 请求
func (d *Dispatcher) buildRequest(task *model.Notification, vendor *model.VendorConfig) (*http.Request, error) {
	// 序列化 payload
	bodyBytes, err := json.Marshal(task.Payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	req, err := http.NewRequest(vendor.HTTPMethod, vendor.TargetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("new request: %w", err)
	}

	// 设置 headers
	for k, v := range task.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	return req, nil
}

// backoff 指数退避: base * 2^(retryCount-1)
func (d *Dispatcher) backoff(retryCount int) time.Duration {
	base := 1 * time.Second
	return base * time.Duration(1<<uint(retryCount-1))
}
