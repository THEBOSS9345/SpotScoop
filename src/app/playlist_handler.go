package app

import (
	"encoding/json"
	"net/http"

	"spotscoop/src/infra/logs"
	"spotscoop/src/spotify"
)

func (h *Handler) handlePlaylistsRefresh(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	playlists, err := h.spotify.RefreshPlaylists(r.Context())
	if err != nil {
		logs.Error("refresh playlists: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	user := h.currentUser()
	result := make([]map[string]interface{}, len(playlists))
	for i, p := range playlists {
		if user == nil || p.OwnerID != user.ID {
			p.TrackCount = 0
		}
		result[i] = map[string]interface{}{
			"id":          p.ID,
			"name":        p.Name,
			"description": p.Description,
			"imageUrl":    p.ImageURL,
			"trackCount":  p.TrackCount,
			"owner":       p.Owner,
		}
	}

	h.json(w, result)
}

func (h *Handler) handlePlaylistTracksRefresh(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	id := r.PathValue("id")
	songs, err := h.spotify.RefreshPlaylistTracks(r.Context(), id)
	if err != nil {
		logs.Error("refresh tracks: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result := make([]map[string]interface{}, len(songs))
	for i, s := range songs {
		result[i] = map[string]interface{}{
			"id":         s.ID,
			"title":      s.Title,
			"artist":     s.Artist,
			"album":      s.Album,
			"duration":   s.Duration,
			"albumArt":   s.AlbumArt,
			"trackNum":   s.TrackNum,
			"playlistId": s.PlaylistID,
		}
	}
	h.json(w, result)
}

func (h *Handler) handlePlaylists(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	playlists, err := h.spotify.GetPlaylists(r.Context())

	if err != nil {
		logs.Error("fetch playlists: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	user := h.currentUser()
	result := make([]map[string]interface{}, len(playlists))

	for i, p := range playlists {

		if user == nil || p.OwnerID != user.ID {
			p.TrackCount = 0
		}

		result[i] = map[string]interface{}{
			"id":          p.ID,
			"name":        p.Name,
			"description": p.Description,
			"imageUrl":    p.ImageURL,
			"trackCount":  p.TrackCount,
			"owner":       p.Owner,
		}
	}

	h.json(w, result)
}

func (h *Handler) handlePlaylistImport(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	var body struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	playlistID := spotify.ParsePlaylistID(body.URL)
	if playlistID == "" {
		http.Error(w, "invalid playlist URL or ID", http.StatusBadRequest)
		return
	}

	pl, err := h.spotify.GetPlaylistByID(r.Context(), playlistID)
	if err != nil {
		logs.Error("import playlist: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	songs, err := h.spotify.RefreshPlaylistTracks(r.Context(), playlistID)
	if err != nil {
		logs.Error("import playlist tracks: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	songResults := make([]map[string]interface{}, len(songs))
	for i, s := range songs {
		songResults[i] = map[string]interface{}{
			"id":         s.ID,
			"title":      s.Title,
			"artist":     s.Artist,
			"album":      s.Album,
			"duration":   s.Duration,
			"albumArt":   s.AlbumArt,
			"trackNum":   s.TrackNum,
			"playlistId": s.PlaylistID,
		}
	}

	h.json(w, map[string]interface{}{
		"playlist": map[string]interface{}{
			"id":          pl.ID,
			"name":        pl.Name,
			"description": pl.Description,
			"imageUrl":    pl.ImageURL,
			"trackCount":  pl.TrackCount,
			"owner":       pl.Owner,
		},
		"songs": songResults,
	})
}

func (h *Handler) handlePlaylistTracks(w http.ResponseWriter, r *http.Request) {
	if !h.requireAuth(w, r) {
		return
	}

	id := r.PathValue("id")
	songs, err := h.spotify.GetPlaylistTracks(r.Context(), id)
	if err != nil {
		logs.Error("fetch tracks: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	result := make([]map[string]interface{}, len(songs))
	for i, s := range songs {
		result[i] = map[string]interface{}{
			"id":         s.ID,
			"title":      s.Title,
			"artist":     s.Artist,
			"album":      s.Album,
			"duration":   s.Duration,
			"albumArt":   s.AlbumArt,
			"trackNum":   s.TrackNum,
			"playlistId": s.PlaylistID,
		}
	}
	h.json(w, result)
}
