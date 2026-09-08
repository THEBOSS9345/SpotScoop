package app

import (
	"sync"
	"testing"
	"time"

	"spotscoop/src/domain"
	"spotscoop/src/ytdl"
)

// newTestDownloader builds a Downloader without starting workers or the
// publisher, so state-management can be exercised without reaching the real
// yt-dlp service.
func newTestDownloader() *Downloader {
	d := &Downloader{
		tasks:  make(map[string]*task),
		broker: NewBroker(),
		stopCh: make(chan struct{}),
	}
	d.queueCond = sync.NewCond(&d.queueMu)
	return d
}

// TestCloseReleasesWorkers verifies workers parked on the queue condition wake
// and exit, rather than leaking for the life of the process.
func TestCloseReleasesWorkers(t *testing.T) {
	d := newTestDownloader()

	exited := make(chan struct{}, 3)
	for range 3 {
		go func() {
			for {
				task, ok := d.dequeue()
				if !ok {
					exited <- struct{}{}
					return
				}
				_ = task
			}
		}()
	}

	// Let the workers park on the condition variable before closing.
	time.Sleep(50 * time.Millisecond)
	d.Close()

	for range 3 {
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			t.Fatal("worker did not exit after Close")
		}
	}

	// Close must be idempotent: Shutdown may run more than once.
	d.Close()

	// Enqueueing after close must not resurrect the queue.
	d.enqueue(&task{Download: domain.Download{ID: "late"}})
	d.queueMu.Lock()
	queued := len(d.queue)
	d.queueMu.Unlock()
	if queued != 0 {
		t.Fatalf("expected enqueue after Close to be dropped, got %d queued", queued)
	}
}

func TestPickResultPrefersClosestDuration(t *testing.T) {
	results := []ytdl.SearchResult{
		{Title: "1 HOUR LOOP", Duration: 3600},
		{Title: "official audio", Duration: 214},
		{Title: "live at wembley", Duration: 380},
	}

	got := pickResult(results, 210)
	if got.Title != "official audio" {
		t.Fatalf("expected the 214s track, got %q (%ds)", got.Title, got.Duration)
	}
}

func TestPickResultFallsBackWhenDurationsUnknown(t *testing.T) {
	results := []ytdl.SearchResult{
		{Title: "first", Duration: 0},
		{Title: "second", Duration: 0},
	}

	if got := pickResult(results, 210); got.Title != "first" {
		t.Fatalf("expected fallback to first result, got %q", got.Title)
	}

	// A track with no known duration cannot be matched, so order wins.
	if got := pickResult(results, 0); got.Title != "first" {
		t.Fatalf("expected fallback to first result, got %q", got.Title)
	}
}

func TestSortDownloadsOrdersByStatusThenAge(t *testing.T) {
	dl := []domain.Download{
		{ID: "done", Status: domain.DownloadComplete, CreatedAt: 1},
		{ID: "newer", Status: domain.DownloadDownloading, CreatedAt: 20},
		{ID: "failed", Status: domain.DownloadFailed, CreatedAt: 1},
		{ID: "older", Status: domain.DownloadDownloading, CreatedAt: 10},
	}

	sortDownloads(dl)

	want := []string{"older", "newer", "done", "failed"}
	for i, id := range want {
		if dl[i].ID != id {
			t.Fatalf("position %d: want %q, got %q", i, id, dl[i].ID)
		}
	}
}

// TestRetryClaimsTaskOnce guards the TOCTOU that previously let two concurrent
// retries queue the same task twice.
func TestRetryClaimsTaskOnce(t *testing.T) {
	d := newTestDownloader()
	d.tasks["a"] = &task{Download: domain.Download{
		ID:     "a",
		Status: domain.DownloadFailed,
		Error:  "boom",
	}}

	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			d.Retry([]string{"a"})
		}()
	}
	wg.Wait()

	d.queueMu.Lock()
	queued := len(d.queue)
	d.queueMu.Unlock()

	if queued != 1 {
		t.Fatalf("expected the task to be queued exactly once, got %d", queued)
	}

	if got := d.tasks["a"].Download.Error; got != "" {
		t.Fatalf("expected retry to clear the error, got %q", got)
	}
}

func TestRetrySkipsNonFailedTasks(t *testing.T) {
	d := newTestDownloader()
	d.tasks["a"] = &task{Download: domain.Download{ID: "a", Status: domain.DownloadComplete}}
	d.tasks["b"] = &task{Download: domain.Download{ID: "b", Status: domain.DownloadDownloading}}

	d.Retry([]string{"a", "b", "missing"})

	d.queueMu.Lock()
	queued := len(d.queue)
	d.queueMu.Unlock()

	if queued != 0 {
		t.Fatalf("expected nothing queued, got %d", queued)
	}
}

// TestConcurrentStateAccess is the regression test for the data race: readers
// and mutators must all go through d.mu. Run with -race.
func TestConcurrentStateAccess(t *testing.T) {
	d := newTestDownloader()
	for _, id := range []string{"a", "b", "c", "d"} {
		d.tasks[id] = &task{Download: domain.Download{
			ID:     id,
			Song:   domain.Song{Title: "t", Artist: "a"},
			Status: domain.DownloadFailed,
		}}
	}

	ids := []string{"a", "b", "c", "d"}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	loop := func(fn func()) {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				fn()
			}
		}
	}

	// Readers, mirroring the SSE/TUI publish path.
	wg.Add(3)
	go loop(func() { d.GetAll() })
	go loop(func() { d.GetActive() })
	go loop(func() { d.flush() })

	// Stand in for the workers: release each claimed task back to "failed" so
	// Retry keeps re-claiming and rewriting it, sustaining write pressure for
	// the whole run rather than mutating each task only once.
	wg.Add(1)
	go loop(func() {
		d.mu.Lock()
		for _, t := range d.tasks {
			t.running = false
			t.Download.Status = domain.DownloadFailed
		}
		d.mu.Unlock()
		d.drainQueue()
	})

	wg.Add(1)
	go loop(func() { d.Start([]domain.Song{{Title: "x", Artist: "y"}}) })

	for range 4 {
		wg.Add(1)
		go loop(func() { d.Retry(ids) })
	}

	time.Sleep(300 * time.Millisecond)
	close(stop)
	wg.Wait()
}
