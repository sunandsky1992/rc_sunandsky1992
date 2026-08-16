package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"rc_notification/internal/model"
)

// PGStore PostgreSQL 实现
type PGStore struct {
	pool *pgxpool.Pool
}

// NewPGStore 创建 PostgreSQL Store
func NewPGStore(ctx context.Context, databaseURL string) (*PGStore, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ping database: %w", err)
	}
	log.Println("connected to PostgreSQL")
	return &PGStore{pool: pool}, nil
}

// Close 关闭连接池
func (s *PGStore) Close() {
	s.pool.Close()
}

func (s *PGStore) CreateNotification(ctx context.Context, n *model.Notification) error {
	headersJSON, _ := json.Marshal(n.Headers)
	payloadJSON, _ := json.Marshal(n.Payload)

	_, err := s.pool.Exec(ctx,
		`INSERT INTO notifications (notification_id, vendor_id, idempotency_key, headers, payload, status, retry_count, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		n.NotificationID, n.VendorID, n.IdempotencyKey, headersJSON, payloadJSON,
		n.Status, n.RetryCount, n.CreatedAt, n.UpdatedAt,
	)

	if err != nil {
		if pgErr, ok := err.(*pgconn.PgError); ok && pgErr.Code == "23505" {
			return fmt.Errorf("notification already exists")
		}
		return fmt.Errorf("insert notification: %w", err)
	}
	return nil
}

func (s *PGStore) GetNotification(ctx context.Context, notificationID string) (*model.Notification, error) {
	return s.queryNotification(ctx, `SELECT id, notification_id, vendor_id, idempotency_key, headers, payload, status, retry_count, next_retry_at, response_status, response_body, created_at, updated_at FROM notifications WHERE notification_id = $1`, notificationID)
}

func (s *PGStore) GetNotificationByIdempotencyKey(ctx context.Context, key string) (*model.Notification, error) {
	if key == "" {
		return nil, nil
	}
	return s.queryNotification(ctx, `SELECT id, notification_id, vendor_id, idempotency_key, headers, payload, status, retry_count, next_retry_at, response_status, response_body, created_at, updated_at FROM notifications WHERE idempotency_key = $1`, key)
}

func (s *PGStore) queryNotification(ctx context.Context, query string, args ...interface{}) (*model.Notification, error) {
	row := s.pool.QueryRow(ctx, query, args...)

	var n model.Notification
	var headersJSON, payloadJSON []byte
	var nextRetryAt *time.Time

	err := row.Scan(&n.ID, &n.NotificationID, &n.VendorID, &n.IdempotencyKey, &headersJSON, &payloadJSON, &n.Status, &n.RetryCount, &nextRetryAt, &n.ResponseStatus, &n.ResponseBody, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("not found")
		}
		return nil, fmt.Errorf("query notification: %w", err)
	}

	if headersJSON != nil {
		json.Unmarshal(headersJSON, &n.Headers)
	}
	if payloadJSON != nil {
		var payload interface{}
		json.Unmarshal(payloadJSON, &payload)
		n.Payload = payload
	}
	n.NextRetryAt = nextRetryAt
	return &n, nil
}

func (s *PGStore) ClaimTask(ctx context.Context) (*model.Notification, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	query := `SELECT id, notification_id, vendor_id, idempotency_key, headers, payload, status, retry_count, next_retry_at, response_status, response_body, created_at, updated_at
		FROM notifications
		WHERE status = 'pending'
		   OR (status = 'retrying' AND next_retry_at <= NOW())
		   OR (status = 'in_flight' AND updated_at < NOW() - INTERVAL '60 seconds')
		ORDER BY created_at
		LIMIT 1
		FOR UPDATE SKIP LOCKED`

	row := tx.QueryRow(ctx, query)

	var n model.Notification
	var headersJSON, payloadJSON []byte
	var nextRetryAt *time.Time

	err = row.Scan(&n.ID, &n.NotificationID, &n.VendorID, &n.IdempotencyKey, &headersJSON, &payloadJSON, &n.Status, &n.RetryCount, &nextRetryAt, &n.ResponseStatus, &n.ResponseBody, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query task: %w", err)
	}

	if headersJSON != nil {
		json.Unmarshal(headersJSON, &n.Headers)
	}
	if payloadJSON != nil {
		var payload interface{}
		json.Unmarshal(payloadJSON, &payload)
		n.Payload = payload
	}
	n.NextRetryAt = nextRetryAt

	// 更新状态为 in_flight
	_, err = tx.Exec(ctx, `UPDATE notifications SET status = 'in_flight', updated_at = NOW() WHERE id = $1`, n.ID)
	if err != nil {
		return nil, fmt.Errorf("update in_flight: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}

	n.Status = model.StatusInFlight
	return &n, nil
}

func (s *PGStore) UpdateStatus(ctx context.Context, notificationID string, status model.NotificationStatus, retryCount int, nextRetryAt *time.Time) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE notifications SET status = $1, retry_count = $2, next_retry_at = $3, updated_at = NOW() WHERE notification_id = $4`,
		status, retryCount, nextRetryAt, notificationID)
	return err
}

func (s *PGStore) UpdateResponse(ctx context.Context, notificationID string, statusCode int, body string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE notifications SET response_status = $1, response_body = $2 WHERE notification_id = $3`,
		statusCode, body, notificationID)
	return err
}

func (s *PGStore) GetVendorConfig(ctx context.Context, vendorID string) (*model.VendorConfig, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT id, vendor_id, target_url, http_method, timeout_ms, created_at, updated_at FROM vendor_configs WHERE vendor_id = $1`,
		vendorID)

	var v model.VendorConfig
	err := row.Scan(&v.ID, &v.VendorID, &v.TargetURL, &v.HTTPMethod, &v.TimeoutMS, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, fmt.Errorf("vendor not found")
		}
		return nil, fmt.Errorf("query vendor: %w", err)
	}
	return &v, nil
}

func (s *PGStore) GetDeadLetters(ctx context.Context) ([]*model.Notification, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, notification_id, vendor_id, idempotency_key, headers, payload, status, retry_count, next_retry_at, response_status, response_body, created_at, updated_at
		FROM notifications WHERE status = 'dead' ORDER BY updated_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("query dead letters: %w", err)
	}
	defer rows.Close()

	var result []*model.Notification
	for rows.Next() {
		var n model.Notification
		var headersJSON, payloadJSON []byte
		var nextRetryAt *time.Time

		if err := rows.Scan(&n.ID, &n.NotificationID, &n.VendorID, &n.IdempotencyKey, &headersJSON, &payloadJSON, &n.Status, &n.RetryCount, &nextRetryAt, &n.ResponseStatus, &n.ResponseBody, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if headersJSON != nil {
			json.Unmarshal(headersJSON, &n.Headers)
		}
		if payloadJSON != nil {
			var payload interface{}
			json.Unmarshal(payloadJSON, &payload)
			n.Payload = payload
		}
		n.NextRetryAt = nextRetryAt
		result = append(result, &n)
	}
	return result, nil
}

func (s *PGStore) RetryDeadLetter(ctx context.Context, notificationID string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE notifications SET status = 'pending', retry_count = 0, next_retry_at = NULL, updated_at = NOW() WHERE notification_id = $1 AND status = 'dead'`,
		notificationID)
	return err
}

func (s *PGStore) GetStatsOverview(ctx context.Context, start, end time.Time) (*model.StatsOverview, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE status = 'delivered') AS delivered,
			COUNT(*) FILTER (WHERE status = 'dead') AS dead,
			COUNT(*) FILTER (WHERE status = 'pending') AS pending,
			COUNT(*) FILTER (WHERE status = 'in_flight') AS in_flight,
			COUNT(*) FILTER (WHERE status = 'retrying') AS retrying
		FROM notifications WHERE created_at BETWEEN $1 AND $2`,
		start, end)

	stats := &model.StatsOverview{}
	err := row.Scan(&stats.Total, &stats.Delivered, &stats.Dead, &stats.Pending, &stats.InFlight, &stats.Retrying)
	if err != nil {
		return nil, fmt.Errorf("query stats: %w", err)
	}
	stats.Failed = stats.Total - stats.Delivered - stats.Pending - stats.InFlight - stats.Retrying
	if stats.Total > 0 {
		stats.SuccessRate = float64(stats.Delivered) / float64(stats.Total)
		stats.FailureRate = float64(stats.Dead) / float64(stats.Total)
	}
	return stats, nil
}

func (s *PGStore) GetStatsByVendor(ctx context.Context, start, end time.Time) ([]*model.StatsByVendor, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT
			vendor_id,
			COUNT(*) AS total,
			COUNT(*) FILTER (WHERE status = 'delivered') AS delivered,
			COUNT(*) FILTER (WHERE status = 'dead') AS dead
		FROM notifications WHERE created_at BETWEEN $1 AND $2
		GROUP BY vendor_id`,
		start, end)
	if err != nil {
		return nil, fmt.Errorf("query stats by vendor: %w", err)
	}
	defer rows.Close()

	var result []*model.StatsByVendor
	for rows.Next() {
		v := &model.StatsByVendor{}
		if err := rows.Scan(&v.VendorID, &v.Total, &v.Delivered, &v.Dead); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		if v.Total > 0 {
			v.SuccessRate = float64(v.Delivered) / float64(v.Total)
		}
		result = append(result, v)
	}
	return result, nil
}

func (s *PGStore) GetRetryDistribution(ctx context.Context, start, end time.Time) ([]*model.RetryDistribution, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT retry_count, COUNT(*) AS total
		FROM notifications WHERE created_at BETWEEN $1 AND $2
		GROUP BY retry_count ORDER BY retry_count`,
		start, end)
	if err != nil {
		return nil, fmt.Errorf("query retry distribution: %w", err)
	}
	defer rows.Close()

	var result []*model.RetryDistribution
	for rows.Next() {
		d := &model.RetryDistribution{}
		if err := rows.Scan(&d.RetryCount, &d.Total); err != nil {
			return nil, fmt.Errorf("scan: %w", err)
		}
		result = append(result, d)
	}
	return result, nil
}
