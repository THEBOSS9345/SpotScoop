package tui

import (
	"sync"
	"testing"
)

// TestShutdownDuringPublish reproduces the shutdown sequence in Run: the state
// channel is cleared and then closed while download workers are still
// publishing. Senders must never write to the closed channel.
func TestShutdownDuringPublish(t *testing.T) {
	for range 200 {
		ch := make(chan DownloadState, 4)
		SetDownloadStateChannel(ch)

		// Drain so the buffer does not simply fill and take the default branch,
		// which would hide a send on a closed channel.
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			for range ch {
			}
		}()

		var wg sync.WaitGroup
		for range 8 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 50 {
					SendDownloadState(DownloadState{Active: 1})
				}
			}()
		}

		SetDownloadStateChannel(nil)
		close(ch)

		wg.Wait()
		<-drained
	}
}
