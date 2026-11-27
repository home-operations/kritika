package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/home-operations/kritika/internal/tuning"
)

// ErrNotFound is a lookup for a row that does not exist.
var ErrNotFound = errors.New("store: not found")

// RecordWebhookDelivery notes that the connection's webhook delivered a
// request that passed verification, at most once every tuning's
// Store.WebhookDeliveryInterval.
func (s *Store) RecordWebhookDelivery(ctx context.Context, connectionID string) error {
	if _, err := s.app.Exec(ctx, `UPDATE connections SET last_webhook_at = now()
		WHERE id = $1 AND (last_webhook_at IS NULL OR last_webhook_at < now() - make_interval(secs => $2))`, connectionID,
		tuning.Get().Store.WebhookDeliveryInterval.Seconds()); err != nil {
		return fmt.Errorf("store: record webhook delivery: %w", err)
	}
	return nil
}

// RecordUnsignedWebhook notes that the connection's webhook delivered a
// request with no signature, at most once every
// Store.WebhookDeliveryInterval: its App has no
// webhook secret, and every delivery it sends is refused.
func (s *Store) RecordUnsignedWebhook(ctx context.Context, connectionID string) error {
	if _, err := s.app.Exec(ctx, `UPDATE connections SET last_unsigned_webhook_at = now()
		WHERE id = $1 AND (last_unsigned_webhook_at IS NULL OR last_unsigned_webhook_at < now() - make_interval(secs => $2))`,
		connectionID, tuning.Get().Store.WebhookDeliveryInterval.Seconds()); err != nil {
		return fmt.Errorf("store: record unsigned webhook: %w", err)
	}
	return nil
}
