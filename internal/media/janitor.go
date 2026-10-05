package media

import (
	"context"
	"log/slog"
	"time"
)

const (
	pendingUploadLifetime = time.Hour
	unusedUploadLifetime  = 24 * time.Hour
	deletionBatchSize     = 100
)

// JanitorStore is the persistence the Janitor needs.
type JanitorStore interface {
	// ExpireAttachments deletes Attachments that were never finalized or never used.
	ExpireAttachments(ctx context.Context, pendingBefore, unusedBefore time.Time) (int, error)
	PendingStorageDeletions(ctx context.Context, limit int) ([]string, error)
	CompleteStorageDeletion(ctx context.Context, key string) error
}

// Janitor removes Objects whose Attachment rows are gone, and expires abandoned
// uploads so they are queued for removal too.
type Janitor struct {
	store   JanitorStore
	storage Storage
	logger  *slog.Logger
	now     func() time.Time
}

func NewJanitor(store JanitorStore, storage Storage, logger *slog.Logger) *Janitor {
	return &Janitor{store: store, storage: storage, logger: logger, now: time.Now}
}

// Run sweeps every interval until ctx ends.
func (j *Janitor) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		j.Sweep(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Sweep runs one cleanup pass. Failures are logged and retried on the next pass.
func (j *Janitor) Sweep(ctx context.Context) {
	now := j.now().UTC()
	if expired, err := j.store.ExpireAttachments(ctx, now.Add(-pendingUploadLifetime), now.Add(-unusedUploadLifetime)); err != nil {
		j.logger.Error("expire attachments", "error", err)
	} else if expired > 0 {
		j.logger.Info("expired attachments", "count", expired)
	}
	keys, err := j.store.PendingStorageDeletions(ctx, deletionBatchSize)
	if err != nil {
		j.logger.Error("list pending object deletions", "error", err)
		return
	}
	for _, key := range keys {
		if err := j.storage.Delete(ctx, key); err != nil {
			j.logger.Error("delete object", "key", key, "error", err)
			continue
		}
		if err := j.store.CompleteStorageDeletion(ctx, key); err != nil {
			j.logger.Error("complete object deletion", "key", key, "error", err)
		}
	}
}
