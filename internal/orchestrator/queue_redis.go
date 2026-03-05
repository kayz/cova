package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type RedisQueueConfig struct {
	URL       string
	Stream    string
	Group     string
	Consumer  string
	MinIdle   time.Duration
	ReadBlock time.Duration
}

type redisQueue struct {
	client   *redis.Client
	stream   string
	group    string
	consumer string
	minIdle  time.Duration
	block    time.Duration
	cursor   string
}

func NewRedisQueue(ctx context.Context, cfg RedisQueueConfig) (jobQueue, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("redis url is required")
	}
	stream := strings.TrimSpace(cfg.Stream)
	if stream == "" {
		stream = "cova:jobs"
	}
	group := strings.TrimSpace(cfg.Group)
	if group == "" {
		group = "cova-orchestrator"
	}
	consumer := strings.TrimSpace(cfg.Consumer)
	if consumer == "" {
		consumer = "worker-1"
	}
	if cfg.MinIdle <= 0 {
		cfg.MinIdle = 30 * time.Second
	}
	if cfg.ReadBlock <= 0 {
		cfg.ReadBlock = time.Second
	}

	opt, err := redis.ParseURL(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	client := redis.NewClient(opt)
	if err := client.Ping(ctx).Err(); err != nil {
		client.Close()
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	if err := client.XGroupCreateMkStream(ctx, stream, group, "0").Err(); err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		client.Close()
		return nil, fmt.Errorf("create consumer group: %w", err)
	}

	return &redisQueue{
		client:   client,
		stream:   stream,
		group:    group,
		consumer: consumer,
		minIdle:  cfg.MinIdle,
		block:    cfg.ReadBlock,
		cursor:   "0-0",
	}, nil
}

func (q *redisQueue) Enqueue(ctx context.Context, jobID string) error {
	return q.client.XAdd(ctx, &redis.XAddArgs{
		Stream: q.stream,
		Values: map[string]any{
			"job_id": jobID,
		},
	}).Err()
}

func (q *redisQueue) Dequeue(ctx context.Context) (queuedJob, error) {
	// Recover one old pending message first.
	msg, ok, err := q.claimPending(ctx)
	if err != nil {
		return queuedJob{}, err
	}
	if ok {
		return q.buildQueuedJob(msg)
	}

	streams, err := q.client.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    q.group,
		Consumer: q.consumer,
		Streams:  []string{q.stream, ">"},
		Count:    1,
		Block:    q.block,
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return queuedJob{}, context.DeadlineExceeded
		}
		return queuedJob{}, err
	}
	if len(streams) == 0 || len(streams[0].Messages) == 0 {
		return queuedJob{}, context.DeadlineExceeded
	}
	return q.buildQueuedJob(streams[0].Messages[0])
}

func (q *redisQueue) claimPending(ctx context.Context) (redis.XMessage, bool, error) {
	result, next, err := q.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream:   q.stream,
		Group:    q.group,
		Consumer: q.consumer,
		MinIdle:  q.minIdle,
		Start:    q.cursor,
		Count:    1,
	}).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return redis.XMessage{}, false, nil
		}
		return redis.XMessage{}, false, err
	}
	q.cursor = next
	if len(result) == 0 {
		return redis.XMessage{}, false, nil
	}
	return result[0], true, nil
}

func (q *redisQueue) buildQueuedJob(msg redis.XMessage) (queuedJob, error) {
	jobID, _ := msg.Values["job_id"].(string)
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return queuedJob{}, errors.New("redis queue message missing job_id")
	}
	return queuedJob{
		ID: jobID,
		Ack: func(ctx context.Context) error {
			return q.client.XAck(ctx, q.stream, q.group, msg.ID).Err()
		},
	}, nil
}

func (q *redisQueue) Depth(ctx context.Context) int {
	n, err := q.client.XLen(ctx, q.stream).Result()
	if err != nil || n < 0 {
		return 0
	}
	return int(n)
}

func (q *redisQueue) Close() error {
	return q.client.Close()
}
