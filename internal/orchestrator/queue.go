package orchestrator

import (
	"context"
	"errors"
	"sync"
)

type queuedJob struct {
	ID  string
	Ack func(ctx context.Context) error
}

type jobQueue interface {
	Enqueue(ctx context.Context, jobID string) error
	Dequeue(ctx context.Context) (queuedJob, error)
	Depth(ctx context.Context) int
	Close() error
}

type memoryQueue struct {
	ch     chan string
	mu     sync.RWMutex
	closed bool
	once   sync.Once
}

func newMemoryQueue(size int) *memoryQueue {
	if size <= 0 {
		size = 256
	}
	return &memoryQueue{
		ch: make(chan string, size),
	}
}

func (q *memoryQueue) Enqueue(ctx context.Context, jobID string) error {
	q.mu.RLock()
	closed := q.closed
	q.mu.RUnlock()
	if closed {
		return errors.New("queue closed")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case q.ch <- jobID:
		return nil
	}
}

func (q *memoryQueue) Dequeue(ctx context.Context) (queuedJob, error) {
	select {
	case <-ctx.Done():
		return queuedJob{}, ctx.Err()
	case jobID, ok := <-q.ch:
		if !ok {
			return queuedJob{}, errors.New("queue closed")
		}
		return queuedJob{
			ID: jobID,
			Ack: func(ctx context.Context) error {
				_ = ctx
				return nil
			},
		}, nil
	}
}

func (q *memoryQueue) Depth(ctx context.Context) int {
	_ = ctx
	return len(q.ch)
}

func (q *memoryQueue) Close() error {
	q.once.Do(func() {
		q.mu.Lock()
		q.closed = true
		q.mu.Unlock()
	})
	return nil
}
