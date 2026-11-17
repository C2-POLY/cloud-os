package tasks_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	. "github.com/C2-POLY/cloud-os/core/internal/tasks"
	"github.com/C2-POLY/cloud-os/core/internal/testdb"
)

func newQueue(t *testing.T) *Queue {
	t.Helper()
	pool, _ := testdb.Migrated(t)
	return &Queue{Pool: pool}
}

func mustEnqueue(t *testing.T, q *Queue, kind string, opt EnqueueOptions) int64 {
	t.Helper()
	id, ok, err := q.Enqueue(context.Background(), kind, map[string]string{"k": kind}, opt)
	if err != nil || !ok {
		t.Fatalf("enqueue: %d %v %v", id, ok, err)
	}
	return id
}

func TestLifecycle(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	id := mustEnqueue(t, q, "thumb", EnqueueOptions{})

	if task, err := q.Claim(ctx, "w1", []string{"ocr"}); err != nil || task != nil {
		t.Fatalf("claim another kind: %+v, %v", task, err)
	}
	task, err := q.Claim(ctx, "w1", []string{"thumb"})
	if err != nil || task == nil || task.ID != id || task.State != "running" || task.Attempts != 1 || *task.LockedBy != "w1" {
		t.Fatalf("claim: %+v, %v", task, err)
	}
	if string(task.Payload) != `{"k": "thumb"}` {
		t.Errorf("payload %s", task.Payload)
	}
	if again, _ := q.Claim(ctx, "w2", []string{"thumb"}); again != nil {
		t.Fatal("a running task was claimed twice")
	}
	if err := q.Extend(ctx, id, "w2"); !errors.Is(err, ErrLost) {
		t.Errorf("another worker extends: %v", err)
	}
	if err := q.Extend(ctx, id, "w1"); err != nil {
		t.Fatal(err)
	}
	if err := q.Complete(ctx, id, "w1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := q.Get(ctx, id); got.State != "done" || got.LockedBy != nil {
		t.Errorf("after complete: %+v", got)
	}
	if err := q.Complete(ctx, id, "w1"); !errors.Is(err, ErrLost) {
		t.Errorf("complete twice: %v", err)
	}
}

func TestFailRetriesWithBackoffThenGivesUp(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	id := mustEnqueue(t, q, "ocr", EnqueueOptions{MaxAttempts: 3})

	var delays []time.Duration
	for attempt := 1; attempt <= 3; attempt++ {
		// Make the task ready now, whatever its backoff.
		if _, err := q.Pool.Exec(ctx, `UPDATE tasks SET run_after = now() WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
		task, err := q.Claim(ctx, "w", []string{"ocr"})
		if err != nil || task == nil || task.Attempts != attempt {
			t.Fatalf("attempt %d: %+v, %v", attempt, task, err)
		}
		if err := q.Fail(ctx, id, "w", fmt.Sprintf("boom %d", attempt)); err != nil {
			t.Fatal(err)
		}
		var state string
		var delay float64
		if err := q.Pool.QueryRow(ctx, `SELECT state, extract(epoch FROM run_after - updated_at) FROM tasks WHERE id = $1`, id).Scan(&state, &delay); err != nil {
			t.Fatal(err)
		}
		if attempt < 3 {
			if state != "queued" {
				t.Errorf("attempt %d: state %s, want queued", attempt, state)
			}
			delays = append(delays, time.Duration(delay*float64(time.Second)).Round(time.Second))
		} else if state != "failed" {
			t.Errorf("last attempt: state %s, want failed", state)
		}
	}
	if fmt.Sprint(delays) != "[10s 20s]" {
		t.Errorf("backoff = %v, want [10s 20s]", delays)
	}
	if got, _ := q.Get(ctx, id); got.LastError == nil || *got.LastError != "boom 3" {
		t.Errorf("last error %v", got.LastError)
	}
	// A task waiting for its backoff is not claimed early.
	id2 := mustEnqueue(t, q, "ocr", EnqueueOptions{})
	task, _ := q.Claim(ctx, "w", []string{"ocr"})
	_ = q.Fail(ctx, task.ID, "w", "x")
	if early, _ := q.Claim(ctx, "w", []string{"ocr"}); early != nil {
		t.Errorf("task %d claimed during its backoff", id2)
	}
}

func TestReapGivesBackTheTasksOfADeadWorker(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	alive := mustEnqueue(t, q, "x", EnqueueOptions{})
	dead := mustEnqueue(t, q, "x", EnqueueOptions{})
	last := mustEnqueue(t, q, "x", EnqueueOptions{MaxAttempts: 1})
	for range 3 {
		if task, err := q.Claim(ctx, "w", []string{"x"}); err != nil || task == nil {
			t.Fatal(err)
		}
	}
	// dead and last stopped answering; alive still holds its lease.
	if _, err := q.Pool.Exec(ctx, `UPDATE tasks SET locked_until = now() - interval '1 second' WHERE id = ANY($1)`, []int64{dead, last}); err != nil {
		t.Fatal(err)
	}
	if n, err := q.Reap(ctx); err != nil || n != 2 {
		t.Fatalf("reap = %d, %v", n, err)
	}
	for id, want := range map[int64]string{alive: "running", dead: "queued", last: "failed"} {
		if got, _ := q.Get(ctx, id); got.State != want {
			t.Errorf("task %d: %s, want %s", id, got.State, want)
		}
	}
	// The dead worker comes back: it no longer holds its task.
	if err := q.Complete(ctx, dead, "w"); !errors.Is(err, ErrLost) {
		t.Errorf("complete after reap: %v", err)
	}
}

func TestDedupeAndPriority(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	first := mustEnqueue(t, q, "thumb", EnqueueOptions{DedupeKey: "thumb:42"})
	if id, ok, err := q.Enqueue(ctx, "thumb", nil, EnqueueOptions{DedupeKey: "thumb:42"}); err != nil || ok || id != 0 {
		t.Errorf("duplicate while queued: %d %v %v", id, ok, err)
	}
	task, _ := q.Claim(ctx, "w", []string{"thumb"})
	if id, ok, _ := q.Enqueue(ctx, "thumb", nil, EnqueueOptions{DedupeKey: "thumb:42"}); ok {
		t.Errorf("duplicate while running: %d", id)
	}
	_ = q.Complete(ctx, task.ID, "w")
	if _, ok, _ := q.Enqueue(ctx, "thumb", nil, EnqueueOptions{DedupeKey: "thumb:42"}); !ok {
		t.Error("the key should be free once the task is done")
	}
	_ = first

	// New files go first, then arrival order.
	q2 := newQueue(t)
	old1 := mustEnqueue(t, q2, "p", EnqueueOptions{Priority: PriorityBackfill})
	newer := mustEnqueue(t, q2, "p", EnqueueOptions{Priority: PriorityNew})
	old2 := mustEnqueue(t, q2, "p", EnqueueOptions{Priority: PriorityBackfill})
	var order []int64
	for range 3 {
		task, _ := q2.Claim(ctx, "w", []string{"p"})
		order = append(order, task.ID)
	}
	if fmt.Sprint(order) != fmt.Sprint([]int64{newer, old1, old2}) {
		t.Errorf("order %v, want %v", order, []int64{newer, old1, old2})
	}
	if s, _ := q2.Stats(ctx); s != (Stats{Running: 3}) {
		t.Errorf("stats %+v", s)
	}
}

// TestConcurrentWorkersNeverShareATask runs 8 workers over 200 tasks.
func TestConcurrentWorkersNeverShareATask(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	const n = 200
	for range n {
		mustEnqueue(t, q, "c", EnqueueOptions{})
	}
	var mu sync.Mutex
	seen := map[int64]string{}
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("w%d", w)
			for {
				task, err := q.Claim(ctx, name, []string{"c"})
				if err != nil {
					t.Error(err)
					return
				}
				if task == nil {
					return
				}
				mu.Lock()
				if other, dup := seen[task.ID]; dup {
					t.Errorf("task %d taken by %s and %s", task.ID, other, name)
				}
				seen[task.ID] = name
				mu.Unlock()
				if err := q.Complete(ctx, task.ID, name); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	if len(seen) != n {
		t.Errorf("%d tasks run, want %d", len(seen), n)
	}
	if s, _ := q.Stats(ctx); s != (Stats{}) {
		t.Errorf("left: %+v", s)
	}
}
