// Package tasks 是一个简单的后台任务队列：按 key 去重、有并发上限、可查询状态。
package tasks

import (
	"context"
	"log"
	"sync"
	"time"
)

type task struct {
	key   string
	label string
	fn    func(ctx context.Context) error
}

// Queue 是一个有固定 worker 数的任务队列。
type Queue struct {
	name string

	mu      sync.Mutex
	cond    *sync.Cond
	pending []*task
	queued  map[string]bool
	running map[string]string // key → label
	done    int
	failed  int
	errors  []ErrorEntry
	closed  bool
	wg      sync.WaitGroup
}

type ErrorEntry struct {
	Label string `json:"label"`
	Err   string `json:"err"`
	Time  int64  `json:"time"`
}

type Status struct {
	Name    string       `json:"name"`
	Pending int          `json:"pending"`
	Running []string     `json:"running"`
	Done    int          `json:"done"`
	Failed  int          `json:"failed"`
	Errors  []ErrorEntry `json:"errors"`
}

// New 创建队列并启动 workers 个工作协程。
func New(ctx context.Context, name string, workers int) *Queue {
	q := &Queue{name: name, queued: map[string]bool{}, running: map[string]string{}}
	q.cond = sync.NewCond(&q.mu)
	for i := 0; i < workers; i++ {
		q.wg.Add(1)
		go q.worker(ctx)
	}
	go func() {
		<-ctx.Done()
		q.mu.Lock()
		q.closed = true
		q.cond.Broadcast()
		q.mu.Unlock()
	}()
	return q
}

// Add 加入任务。同一个 key 已在排队时忽略（正在运行的不算，允许再排一次）。
func (q *Queue) Add(key, label string, fn func(ctx context.Context) error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.queued[key] {
		return
	}
	q.queued[key] = true
	q.pending = append(q.pending, &task{key: key, label: label, fn: fn})
	q.cond.Signal()
}

func (q *Queue) worker(ctx context.Context) {
	defer q.wg.Done()
	for {
		q.mu.Lock()
		for len(q.pending) == 0 && !q.closed {
			q.cond.Wait()
		}
		if q.closed {
			q.mu.Unlock()
			return
		}
		t := q.pending[0]
		q.pending = q.pending[1:]
		delete(q.queued, t.key)
		q.running[t.key] = t.label
		q.mu.Unlock()

		err := safeRun(ctx, t)

		q.mu.Lock()
		delete(q.running, t.key)
		q.done++
		if err != nil && ctx.Err() == nil {
			q.failed++
			q.errors = append(q.errors, ErrorEntry{Label: t.label, Err: err.Error(), Time: time.Now().Unix()})
			if len(q.errors) > 50 {
				q.errors = q.errors[len(q.errors)-50:]
			}
			log.Printf("[%s] %s 失败: %v", q.name, t.label, err)
		}
		q.cond.Broadcast()
		q.mu.Unlock()
	}
}

func safeRun(ctx context.Context, t *task) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("任务 %s 崩溃: %v", t.label, r)
			err = errPanic
		}
	}()
	return t.fn(ctx)
}

type panicErr struct{}

func (panicErr) Error() string { return "任务异常退出" }

var errPanic error = panicErr{}

func (q *Queue) Status() Status {
	q.mu.Lock()
	defer q.mu.Unlock()
	s := Status{Name: q.name, Pending: len(q.pending), Done: q.done, Failed: q.failed,
		Errors: append([]ErrorEntry(nil), q.errors...)}
	for _, l := range q.running {
		s.Running = append(s.Running, l)
	}
	return s
}

// Idle 报告队列是否空闲。
func (q *Queue) Idle() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.pending) == 0 && len(q.running) == 0
}

// Wait 等待队列空闲（测试用）。
func (q *Queue) Wait(ctx context.Context) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	stop := context.AfterFunc(ctx, func() {
		q.mu.Lock()
		q.cond.Broadcast()
		q.mu.Unlock()
	})
	defer stop()
	for len(q.pending) > 0 || len(q.running) > 0 {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		q.cond.Wait()
	}
	return nil
}
