package app

import (
	"encoding/json"
	"fmt"
	"sort"

	"spotscoop/src/domain"
)

type DownloadState struct {
	Downloads []map[string]interface{} `json:"downloads"`
	Queue     []map[string]interface{} `json:"queue"`
	Active    int                      `json:"active"`
	Queued    int                      `json:"queued"`
}

var statusOrder = map[domain.DownloadStatus]int{
	domain.DownloadDownloading: 0,
	domain.DownloadConverting:  1,
	domain.DownloadSearching:   2,
	domain.DownloadPending:     3,
	domain.DownloadComplete:    4,
	domain.DownloadFailed:      5,
}

func sortDownloads(dl []domain.Download) {
	sort.SliceStable(dl, func(i, j int) bool {
		si, sj := statusOrder[dl[i].Status], statusOrder[dl[j].Status]
		if si != sj {
			return si < sj
		}
		return dl[i].CreatedAt < dl[j].CreatedAt
	})
}

func downloadToMap(d domain.Download) map[string]interface{} {
	m := map[string]interface{}{
		"id":     d.ID,
		"status": string(d.Status),
		"progress": d.Progress,
		"error":  d.Error,
		"song": map[string]interface{}{
			"title":  d.Song.Title,
			"artist": d.Song.Artist,
			"album":  d.Song.Album,
		},
	}
	if d.CreatedAt > 0 {
		m["createdAt"] = d.CreatedAt
	}
	return m
}

func queueToMap(d domain.Download) map[string]interface{} {
	return map[string]interface{}{
		"song": map[string]interface{}{
			"title":  d.Song.Title,
			"artist": d.Song.Artist,
			"album":  d.Song.Album,
		},
	}
}

func BuildDownloadState(all []domain.Download, active []domain.Download, queued []domain.Download) DownloadState {
	sorted := make([]domain.Download, len(all))
	copy(sorted, all)
	sortDownloads(sorted)

	result := make([]map[string]interface{}, len(sorted))
	for i, d := range sorted {
		result[i] = downloadToMap(d)
	}

	queueItems := make([]map[string]interface{}, len(queued))
	for i, q := range queued {
		queueItems[i] = queueToMap(q)
	}

	return DownloadState{
		Downloads: result,
		Queue:     queueItems,
		Active:    len(active),
		Queued:    len(queued),
	}
}

func SSEEventBytes(state DownloadState) []byte {
	raw, _ := json.Marshal(state)
	return []byte(fmt.Sprintf("event: download-state\ndata: %s\n\n", string(raw)))
}
