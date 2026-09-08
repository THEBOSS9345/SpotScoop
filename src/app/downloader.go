package app

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"spotscoop/src/domain"
	"spotscoop/src/infra/logs"
	"spotscoop/src/infra/tui"
	"spotscoop/src/ytdl"

	"github.com/google/uuid"
)

// downloadTimeout bounds a single track's search+download+convert cycle.
const downloadTimeout = 6 * time.Hour

// durationTolerance is how far a YouTube result's length may drift from the
// Spotify track's before we prefer a closer candidate.
const durationTolerance = 15

// publishInterval bounds how often download state is broadcast to SSE clients
// and the TUI, regardless of how fast progress callbacks arrive.
const publishInterval = 150 * time.Millisecond

type task struct {
	Download domain.Download
	// cancel aborts the in-flight run; nil while the task sits in the queue.
	// Guarded by Downloader.mu.
	cancel context.CancelFunc
	// running is set while the task is claimed by a worker, so a concurrent
	// Retry cannot enqueue the same task twice. Guarded by Downloader.mu.
	running bool
}

type Downloader struct {
	ytdl   *ytdl.Service
	broker *Broker

	mu    sync.RWMutex
	tasks map[string]*task

	queueMu   sync.Mutex
	queueCond *sync.Cond
	queue     []*task
	closed    bool

	dirty  atomic.Bool
	stopCh chan struct{}
}

func NewDownloader(ytdl *ytdl.Service, broker *Broker, maxConcurrent int) *Downloader {
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	d := &Downloader{
		ytdl:   ytdl,
		tasks:  make(map[string]*task),
		broker: broker,
		stopCh: make(chan struct{}),
	}
	d.queueCond = sync.NewCond(&d.queueMu)

	// A fixed worker pool replaces a goroutine-per-song: importing a large
	// playlist no longer allocates thousands of goroutines and timers that
	// only sit blocked waiting for a slot.
	for range maxConcurrent {
		go d.worker()
	}
	go d.publisher()
	return d
}

func (d *Downloader) enqueue(t *task) {
	d.queueMu.Lock()
	if d.closed {
		d.queueMu.Unlock()
		return
	}
	d.queue = append(d.queue, t)
	d.queueMu.Unlock()
	d.queueCond.Signal()
}

// dequeue blocks for the next task, returning false once the downloader is
// closed so workers can exit instead of parking on the condition forever.
func (d *Downloader) dequeue() (*task, bool) {
	d.queueMu.Lock()
	defer d.queueMu.Unlock()
	for len(d.queue) == 0 && !d.closed {
		d.queueCond.Wait()
	}
	if d.closed {
		return nil, false
	}
	t := d.queue[0]
	d.queue[0] = nil
	d.queue = d.queue[1:]
	return t, true
}

func (d *Downloader) drainQueue() {
	d.queueMu.Lock()
	for i := range d.queue {
		d.queue[i] = nil
	}
	d.queue = nil
	d.queueMu.Unlock()
}

func (d *Downloader) worker() {
	for {
		t, ok := d.dequeue()
		if !ok {
			return
		}
		d.run(t)
	}
}

// Close stops the worker pool and publisher and cancels anything in flight.
// The Downloader is not usable afterwards.
func (d *Downloader) Close() {
	d.queueMu.Lock()
	if d.closed {
		d.queueMu.Unlock()
		return
	}
	d.closed = true
	for i := range d.queue {
		d.queue[i] = nil
	}
	d.queue = nil
	d.queueMu.Unlock()
	d.queueCond.Broadcast()

	close(d.stopCh)

	d.mu.Lock()
	for _, t := range d.tasks {
		if t.cancel != nil {
			t.cancel()
		}
	}
	d.mu.Unlock()
}

// run owns the lifetime of one attempt: it creates the timeout context,
// publishes the cancel func so Clear can abort it, and always releases the
// context when the attempt ends.
func (d *Downloader) run(t *task) {
	ctx, cancel := context.WithTimeout(context.Background(), downloadTimeout)
	defer cancel()

	d.mu.Lock()
	// The task may have been cleared while it waited in the queue.
	if _, live := d.tasks[t.Download.ID]; !live {
		t.running = false
		d.mu.Unlock()
		return
	}
	t.cancel = cancel
	t.running = true
	d.mu.Unlock()

	d.process(ctx, t)

	d.mu.Lock()
	t.cancel = nil
	t.running = false
	d.mu.Unlock()
}

// publishState marks the state dirty rather than rebuilding it. Progress
// callbacks fire every 200ms per active download, and each rebuild sorts and
// re-marshals every task, so the publisher goroutine coalesces bursts into at
// most one broadcast per publishInterval.
func (d *Downloader) publishState() {
	d.dirty.Store(true)
}

func (d *Downloader) publisher() {
	ticker := time.NewTicker(publishInterval)
	defer ticker.Stop()
	for {
		select {
		case <-d.stopCh:
			return
		case <-ticker.C:
			if d.dirty.Swap(false) {
				d.flush()
			}
		}
	}
}

// flush builds and broadcasts the current state immediately. It deliberately
// does not clear the dirty flag: a mutation landing mid-flush must survive to
// trigger the next tick rather than being swallowed here.
func (d *Downloader) flush() {
	all := d.GetAll()
	active, queued := d.GetActive()

	if d.broker.HasClients() {
		d.broker.Publish(BuildDownloadState(all, active, queued))
	}

	tracks := make([]tui.TrackState, len(all))
	var completeCount, failedCount int
	for i, dl := range all {
		tracks[i] = tui.TrackState{
			ID:              dl.ID,
			Title:           dl.Song.Title,
			Artist:          dl.Song.Artist,
			Status:          string(dl.Status),
			Progress:        dl.Progress,
			DownloadedBytes: dl.DownloadedBytes,
			TotalBytes:      dl.TotalBytes,
			Error:           dl.Error,
		}
		switch dl.Status {
		case domain.DownloadComplete:
			completeCount++
		case domain.DownloadFailed:
			failedCount++
		}
	}
	tui.SendDownloadState(tui.DownloadState{
		Tracks:   tracks,
		Active:   len(active),
		Queued:   len(queued),
		Complete: completeCount,
		Failed:   failedCount,
	})
}

func (d *Downloader) Start(songs []domain.Song) string {
	batchID := uuid.NewString()
	for i := range songs {
		t := &task{Download: domain.Download{
			ID:        uuid.NewString(),
			BatchID:   batchID,
			Song:      songs[i],
			Status:    domain.DownloadPending,
			CreatedAt: time.Now().Unix(),
		}}
		d.mu.Lock()
		d.tasks[t.Download.ID] = t
		d.mu.Unlock()
		d.enqueue(t)
	}
	d.publishState()
	return batchID
}

func (d *Downloader) GetActive() (active []domain.Download, queued []domain.Download) {
	d.mu.RLock()
	for _, t := range d.tasks {
		switch t.Download.Status {
		case domain.DownloadSearching, domain.DownloadDownloading, domain.DownloadConverting:
			active = append(active, t.Download)
		case domain.DownloadPending:
			queued = append(queued, t.Download)
		}
	}
	d.mu.RUnlock()
	return
}

func (d *Downloader) GetAll() []domain.Download {
	d.mu.RLock()
	tasks := make([]domain.Download, 0, len(d.tasks))
	for _, t := range d.tasks {
		tasks = append(tasks, t.Download)
	}
	d.mu.RUnlock()
	return tasks
}

func (d *Downloader) CleanupStale() {
	d.ytdl.CleanupOrphans()
	d.flush()
}

func (d *Downloader) Retry(ids []string) {
	var requeued int
	for _, id := range ids {
		d.mu.Lock()
		t, exists := d.tasks[id]
		if !exists || t.running || t.Download.Status != domain.DownloadFailed {
			d.mu.Unlock()
			continue
		}
		// Claim the task under the same lock that observed it as failed, so
		// two concurrent retries cannot both queue it.
		t.running = true
		t.Download.Status = domain.DownloadPending
		t.Download.Error = ""
		t.Download.Progress = 0
		t.Download.DownloadedBytes = 0
		t.Download.TotalBytes = 0
		d.mu.Unlock()

		d.enqueue(t)
		requeued++
	}
	if requeued > 0 {
		d.publishState()
	}
}

func (d *Downloader) Clear() {
	d.drainQueue()

	d.mu.Lock()
	for _, t := range d.tasks {
		if t.cancel != nil {
			t.cancel()
		}
	}
	d.tasks = make(map[string]*task)
	d.mu.Unlock()
	d.flush()
}

// pickResult prefers the candidate closest in length to the Spotify track.
// Without this the first hit wins, which is routinely a live cut, a "1 hour
// loop", or a full album upload rather than the track we asked for.
func pickResult(results []ytdl.SearchResult, wantSeconds int) ytdl.SearchResult {
	if wantSeconds <= 0 {
		return results[0]
	}

	best := results[0]
	bestDiff := -1
	for _, r := range results {
		if r.Duration <= 0 {
			continue
		}
		diff := r.Duration - wantSeconds
		if diff < 0 {
			diff = -diff
		}
		if bestDiff < 0 || diff < bestDiff {
			best, bestDiff = r, diff
		}
	}

	if bestDiff < 0 {
		return results[0]
	}
	if bestDiff > durationTolerance {
		logs.Warning("Closest match for %q is %ds off the expected %ds", best.Title, bestDiff, wantSeconds)
	}
	return best
}

func (d *Downloader) process(ctx context.Context, t *task) {
	// Every mutation of t.Download goes through these helpers so it is always
	// written under the same lock that GetAll/GetActive read it under.
	update := func(p domain.DownloadProgress) {
		d.mu.Lock()
		t.Download.Status = p.Status
		t.Download.Progress = p.Progress
		t.Download.DownloadedBytes = p.DownloadedBytes
		t.Download.TotalBytes = p.TotalBytes
		d.mu.Unlock()
		d.publishState()
	}

	fail := func(format string, args ...any) {
		d.mu.Lock()
		t.Download.Error = fmt.Sprintf(format, args...)
		t.Download.Status = domain.DownloadFailed
		d.mu.Unlock()
		d.publishState()
	}

	update(domain.DownloadProgress{Status: domain.DownloadSearching})

	d.mu.RLock()
	song := t.Download.Song
	d.mu.RUnlock()

	query := fmt.Sprintf("%s - %s", song.Artist, song.Title)
	logs.Info("Searching: %s", query)

	results, err := d.ytdl.Search(ctx, query)
	if err != nil {
		logs.Error("Search failed for %s: %v", query, err)
		fail("Search failed: %v", err)
		return
	}

	if len(results) == 0 {
		fail("No YouTube results found")
		return
	}

	logs.Info("Found %d results for %s", len(results), query)
	chosen := pickResult(results, song.Duration)

	update(domain.DownloadProgress{Status: domain.DownloadDownloading, Progress: 10})
	start := time.Now()

	path, err := d.ytdl.Download(ctx, chosen, song, update)
	if err != nil {
		logs.Error("Download failed for %s: %v", song.Title, err)
		fail("%v", err)
		return
	}

	d.mu.Lock()
	t.Download.OutputPath = path
	t.Download.Status = domain.DownloadComplete
	t.Download.Progress = 100
	d.mu.Unlock()
	d.publishState()

	logs.Success("Finished: %s in %v", song.Title, time.Since(start))
}
