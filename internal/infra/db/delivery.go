package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jmoiron/sqlx"

	"github.com/pj-hoakari/tolo-realtime/internal/domain"
	"github.com/pj-hoakari/tolo-realtime/internal/repository"
)

type PostgresDeliveryLog struct {
	db *sqlx.DB
}

func NewPostgresDeliveryLog(db *sqlx.DB) *PostgresDeliveryLog {
	return &PostgresDeliveryLog{db: db}
}

func (l *PostgresDeliveryLog) Append(ctx context.Context, delivery domain.Delivery) error {
	_, err := Executor(ctx, l.db).ExecContext(ctx, `
		INSERT INTO deliveries (delivery_id, event_id, recipient_user_ids, payload, sent_at)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (delivery_id) DO NOTHING`,
		string(delivery.ID), delivery.EventID, delivery.Recipients, delivery.Payload, delivery.SentAt)
	if err != nil {
		return fmt.Errorf("insert delivery: %w", err)
	}

	return nil
}

func (l *PostgresDeliveryLog) LatestID(ctx context.Context, eventID string) (domain.DeliveryID, error) {
	_, id, err := l.latest(ctx, eventID)

	return id, err
}

func (l *PostgresDeliveryLog) ListAfter(
	ctx context.Context,
	eventID, recipient string,
	after domain.DeliveryID,
	limit int,
) (repository.DeliveryPage, error) {
	var afterSeq int64

	err := sqlx.GetContext(ctx, Executor(ctx, l.db), &afterSeq,
		`SELECT seq FROM deliveries WHERE event_id = $1 AND delivery_id = $2`, eventID, string(after))
	if errors.Is(err, sql.ErrNoRows) {
		return repository.DeliveryPage{}, domain.ErrResumePointNotFound
	}

	if err != nil {
		return repository.DeliveryPage{}, fmt.Errorf("select resume point: %w", err)
	}

	maxSeq, latestID, err := l.latest(ctx, eventID)
	if err != nil {
		return repository.DeliveryPage{}, err
	}

	rows, err := l.list(ctx, `
		SELECT delivery_id, event_id, payload, sent_at FROM deliveries
		WHERE event_id = $1 AND seq > $2 AND seq <= $3 AND $4 = ANY(recipient_user_ids)
		ORDER BY seq
		LIMIT $5`,
		eventID, afterSeq, maxSeq, recipient, limit)
	if err != nil {
		return repository.DeliveryPage{}, err
	}

	if len(rows) == limit {
		latestID = rows[len(rows)-1].ID
	}

	return repository.DeliveryPage{Deliveries: rows, LatestID: latestID}, nil
}

func (l *PostgresDeliveryLog) ListRecent(
	ctx context.Context,
	eventID, recipient string,
	limit int,
) (repository.DeliveryPage, error) {
	maxSeq, latestID, err := l.latest(ctx, eventID)
	if err != nil {
		return repository.DeliveryPage{}, err
	}

	rows, err := l.list(ctx, `
		SELECT delivery_id, event_id, payload, sent_at FROM deliveries
		WHERE event_id = $1 AND seq <= $2 AND $3 = ANY(recipient_user_ids)
		ORDER BY seq DESC
		LIMIT $4`,
		eventID, maxSeq, recipient, limit)
	if err != nil {
		return repository.DeliveryPage{}, err
	}

	slices.Reverse(rows)

	return repository.DeliveryPage{Deliveries: rows, LatestID: latestID}, nil
}

func (l *PostgresDeliveryLog) latest(ctx context.Context, eventID string) (int64, domain.DeliveryID, error) {
	var row struct {
		Seq        int64  `db:"seq"`
		DeliveryID string `db:"delivery_id"`
	}

	err := sqlx.GetContext(ctx, Executor(ctx, l.db), &row,
		`SELECT seq, delivery_id FROM deliveries WHERE event_id = $1 ORDER BY seq DESC LIMIT 1`, eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil
	}

	if err != nil {
		return 0, "", fmt.Errorf("select latest delivery: %w", err)
	}

	return row.Seq, domain.DeliveryID(row.DeliveryID), nil
}

type deliveryRow struct {
	DeliveryID string    `db:"delivery_id"`
	EventID    string    `db:"event_id"`
	Payload    []byte    `db:"payload"`
	SentAt     time.Time `db:"sent_at"`
}

func (l *PostgresDeliveryLog) list(ctx context.Context, query string, args ...any) ([]domain.Delivery, error) {
	var rows []deliveryRow
	if err := sqlx.SelectContext(ctx, Executor(ctx, l.db), &rows, query, args...); err != nil {
		return nil, fmt.Errorf("select deliveries: %w", err)
	}

	deliveries := make([]domain.Delivery, 0, len(rows))

	for _, row := range rows {
		deliveries = append(deliveries, domain.Delivery{
			ID:         domain.DeliveryID(row.DeliveryID),
			EventID:    row.EventID,
			Recipients: nil,
			Payload:    row.Payload,
			SentAt:     row.SentAt.UTC(),
		})
	}

	return deliveries, nil
}
