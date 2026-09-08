package tui

import "sync"

type TrackState struct {
	ID              string
	Title           string
	Artist          string
	Status          string
	Progress        int
	DownloadedBytes int64
	TotalBytes      int64
	Error           string
}

type DownloadState struct {
	Tracks   []TrackState
	Active   int
	Queued   int
	Complete int
	Failed   int
}

// stateMu guards downloadStateCh. Shutdown clears the channel and then closes
// it while download workers may still be publishing; holding the read lock
// across the send guarantees a sender never writes to a closed channel, since
// SetDownloadStateChannel(nil) cannot return until in-flight sends finish.
var (
	stateMu         sync.RWMutex
	downloadStateCh chan DownloadState
)

func SetDownloadStateChannel(ch chan DownloadState) {
	stateMu.Lock()
	downloadStateCh = ch
	stateMu.Unlock()
}

func SendDownloadState(s DownloadState) {
	stateMu.RLock()
	defer stateMu.RUnlock()
	if downloadStateCh == nil {
		return
	}
	select {
	case downloadStateCh <- s:
	default:
	}
}
