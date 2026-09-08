package app

import (
	"encoding/json"
	"net/http"

	"spotscoop/src/domain"
)

func (h *Handler) handleRetry(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	var ids []string
	if err := json.NewDecoder(r.Body).Decode(&ids); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	h.downloader.Retry(ids)
	h.json(w, map[string]bool{"ok": true})
}

func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	var songs []domain.Song
	if err := json.NewDecoder(r.Body).Decode(&songs); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	batchID := h.downloader.Start(songs)
	h.json(w, map[string]string{"batchId": batchID})
}

func (h *Handler) handleDownloads(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}
	h.json(w, h.buildDownloadState())
}

func (h *Handler) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := h.broker.Subscribe()
	defer h.broker.Unsubscribe(ch)

	state := h.buildDownloadState()
	w.Write(SSEEventBytes(state))
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case state, ok := <-ch:
			if !ok {
				return
			}
			w.Write(SSEEventBytes(state))
			flusher.Flush()
		}
	}
}

func (h *Handler) buildDownloadState() DownloadState {
	all := h.downloader.GetAll()
	active, queued := h.downloader.GetActive()
	return BuildDownloadState(all, active, queued)
}
