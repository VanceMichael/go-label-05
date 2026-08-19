package worker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"go-base/internal/store"
)

type Publisher interface {
	Publish(context.Context, string, []byte) error
}

type Outbox struct {
	DB           *store.Database
	Publisher    Publisher
	Interval     time.Duration
	Batch        int
	MaxAttempts  int
	ClaimTimeout time.Duration
	WorkerID     string
	Now          func() time.Time
}

type job struct {
	id       string
	topic    string
	payload  []byte
	attempts int
}

var workerSequence atomic.Uint64

func (w Outbox) Run(ctx context.Context) {
	interval := w.Interval
	if interval <= 0 {
		interval = 2 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = w.RunOnce(ctx)
		}
	}
}

func (w Outbox) RunOnce(ctx context.Context) error {
	if w.DB == nil || w.DB.Pool == nil || w.Publisher == nil {
		return fmt.Errorf("outbox database and publisher are required")
	}
	if w.Batch <= 0 {
		w.Batch = 20
	}
	if w.MaxAttempts <= 0 {
		w.MaxAttempts = 5
	}
	if w.ClaimTimeout <= 0 {
		w.ClaimTimeout = 2 * time.Minute
	}
	if w.WorkerID == "" {
		w.WorkerID = fmt.Sprintf("worker-%d-%d", os.Getpid(), workerSequence.Add(1))
	}
	now := w.now()
	jobs, err := w.claim(ctx, now)
	if err != nil {
		return err
	}
	for _, claimed := range jobs {
		publishErr := w.Publisher.Publish(ctx, claimed.topic, claimed.payload)
		finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err = w.finish(finishCtx, claimed, publishErr, now)
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func (w Outbox) claim(ctx context.Context, now time.Time) ([]job, error) {
	jobs := make([]job, 0, w.Batch)
	err := w.DB.WithTx(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id,topic,payload,attempts
			FROM outbox_jobs
			WHERE (status IN ('pending','retry') AND available_at <= $1)
			   OR (status = 'running' AND locked_at <= $2)
			ORDER BY available_at,id
			FOR UPDATE SKIP LOCKED
			LIMIT $3`, now, now.Add(-w.ClaimTimeout), w.Batch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var claimed job
			if err := rows.Scan(&claimed.id, &claimed.topic, &claimed.payload, &claimed.attempts); err != nil {
				return err
			}
			jobs = append(jobs, claimed)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		for _, claimed := range jobs {
			if _, err := tx.Exec(ctx, `UPDATE outbox_jobs SET status='running',locked_by=$2,locked_at=$3,updated_at=$3 WHERE id=$1`, claimed.id, w.WorkerID, now); err != nil {
				return err
			}
		}
		return nil
	})
	return jobs, err
}

func (w Outbox) finish(ctx context.Context, claimed job, publishErr error, now time.Time) error {
	if publishErr == nil {
		tag, err := w.DB.Pool.Exec(ctx, `
			UPDATE outbox_jobs
			SET status='delivered',attempts=attempts+1,updated_at=$3,last_error='',locked_by='',locked_at=NULL
			WHERE id=$1 AND status='running' AND locked_by=$2`, claimed.id, w.WorkerID, now)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return fmt.Errorf("outbox job %s claim was lost", claimed.id)
		}
		return nil
	}
	status := "retry"
	if claimed.attempts+1 >= w.MaxAttempts {
		status = "dead"
	}
	delay := time.Duration(1<<min(claimed.attempts, 6)) * time.Second
	tag, err := w.DB.Pool.Exec(ctx, `
		UPDATE outbox_jobs
		SET status=$3,attempts=attempts+1,available_at=$4,updated_at=$5,last_error=$6,locked_by='',locked_at=NULL
		WHERE id=$1 AND status='running' AND locked_by=$2`, claimed.id, w.WorkerID, status, now.Add(delay), now, publishErr.Error())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("outbox job %s claim was lost", claimed.id)
	}
	return nil
}

func (w Outbox) now() time.Time {
	if w.Now != nil {
		if now := w.Now(); !now.IsZero() {
			return now.UTC()
		}
	}
	return time.Now().UTC()
}

type LogPublisher struct{}

func (LogPublisher) Publish(ctx context.Context, topic string, payload []byte) error {
	if err := ctx.Err(); err != nil {
		return errors.Join(fmt.Errorf("publish %s", topic), err)
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
