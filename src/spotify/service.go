package spotify

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"spotscoop/src/db"
	"spotscoop/src/domain"
	"spotscoop/src/infra/logs"

	"github.com/zmb3/spotify/v2"
)

// doAPIRequest retries Spotify Web API calls on 429, honoring the
// Retry-After header the API sends when the rate limit is hit.
func doAPIRequest(client *http.Client, req *http.Request, maxAttempts int) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := apiRetryBackoff(attempt)
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(delay):
			}
		}

		resp, err := client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("HTTP 429")
			retryAfter := apiParseRetryAfter(resp.Header.Get("Retry-After"))
			resp.Body.Close()
			if retryAfter == 0 {
				retryAfter = apiRetryBackoff(attempt + 1)
			}
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(retryAfter):
			}
			continue
		}

		return resp, nil
	}
	return nil, fmt.Errorf("request failed after %d attempts: %w", maxAttempts, lastErr)
}

func apiRetryBackoff(attempt int) time.Duration {
	base := time.Duration(1<<uint(attempt-1)) * time.Second
	jitter := time.Duration(rand.Int63n(int64(300 * time.Millisecond)))
	if base > 15*time.Second {
		base = 15 * time.Second
	}
	return base + jitter
}

func apiParseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

type Service struct {
	// clientMu guards client/httpClient: they are swapped by SetClient from the
	// OAuth callback, the startup token loader and logout, while HTTP handlers
	// read them concurrently.
	clientMu   sync.RWMutex
	client     *spotify.Client
	httpClient *http.Client

	db *db.DB

	anon   *anonClient
	anonMu sync.Mutex
}

func New() *Service {
	return &Service{}
}

func (s *Service) SetDB(database *db.DB) {
	s.db = database
}

func (s *Service) SetClient(client *spotify.Client, httpClient *http.Client) {
	s.clientMu.Lock()
	s.client = client
	s.httpClient = httpClient
	s.clientMu.Unlock()
}

// clients returns a consistent snapshot of both clients. Callers must use the
// returned values rather than re-reading the fields, so a concurrent logout
// cannot swap them mid-request.
func (s *Service) clients() (*spotify.Client, *http.Client) {
	s.clientMu.RLock()
	defer s.clientMu.RUnlock()
	return s.client, s.httpClient
}

func (s *Service) IsReady() bool {
	s.clientMu.RLock()
	defer s.clientMu.RUnlock()
	return s.client != nil
}

func (s *Service) GetPlaylists(ctx context.Context) ([]domain.Playlist, error) {
	if client, _ := s.clients(); client == nil {
		return nil, fmt.Errorf("spotify client not initialized")
	}

	if s.db != nil {
		cached, stale, err := s.db.GetCachedPlaylistsStale()
		if err == nil && cached != nil {
			if stale {
				go s.refreshPlaylists()
			}
			return cached, nil
		}
	}

	return s.fetchPlaylists(ctx)
}

func (s *Service) RefreshPlaylists(ctx context.Context) ([]domain.Playlist, error) {
	if client, _ := s.clients(); client == nil {
		return nil, fmt.Errorf("spotify client not initialized")
	}
	return s.fetchPlaylists(ctx)
}

func (s *Service) refreshPlaylists() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, err := s.fetchPlaylists(ctx)
	if err != nil {
		logs.Error("background playlist refresh: %v", err)
	}
}

func (s *Service) fetchPlaylists(ctx context.Context) ([]domain.Playlist, error) {
	client, _ := s.clients()
	if client == nil {
		return nil, fmt.Errorf("spotify client not initialized")
	}

	playlists, err := client.CurrentUsersPlaylists(ctx, spotify.Limit(50))
	if err != nil {
		logs.Error("Failed to fetch playlists: %v", err)
		return nil, err
	}

	result := make([]domain.Playlist, 0, len(playlists.Playlists))
	currentUser, err := client.CurrentUser(ctx)
	if err != nil {
		logs.Error("Failed to get current user: %v", err)
		return nil, err
	}

	for _, p := range playlists.Playlists {
		if string(p.Owner.ID) != currentUser.ID {
			continue
		}

		ownerID := string(p.Owner.ID)
		pl := domain.Playlist{
			ID:          string(p.ID),
			Name:        p.Name,
			Description: p.Description,
			TrackCount:  int(p.Tracks.Total),
			Owner:       p.Owner.DisplayName,
			OwnerID:     ownerID,
		}
		if len(p.Images) > 0 {
			pl.ImageURL = p.Images[0].URL
		}
		result = append(result, pl)
	}

	if s.db != nil {
		if err := s.db.CachePlaylists(result); err != nil {
			logs.Error("failed to cache playlists: %v", err)
		}
	}

	return result, nil
}

type rawItem struct {
	AddedAt string      `json:"added_at"`
	Track   rawItemData `json:"item"`
}

type rawItemData struct {
	ID         string      `json:"id"`
	Name       string      `json:"name"`
	Artists    []rawArtist `json:"artists"`
	Album      rawAlbum    `json:"album"`
	Duration   int         `json:"duration_ms"`
	TrackNum   int         `json:"track_number"`
	DiscNum    int         `json:"disc_number"`
}

type rawArtist struct {
	Name string `json:"name"`
}

type rawAlbum struct {
	Name        string     `json:"name"`
	Images      []rawImage `json:"images"`
	ReleaseDate string     `json:"release_date"`
}

type rawImage struct {
	URL string `json:"url"`
}

type rawPlaylistItemsResponse struct {
	Href  string    `json:"href"`
	Next  string    `json:"next"`
	Items []rawItem `json:"items"`
}

type rawSearchResponse struct {
	Tracks rawSearchTracks `json:"tracks"`
}

type rawSearchTracks struct {
	Items []rawSearchItem `json:"items"`
}

type rawSearchItem struct {
	ID       string      `json:"id"`
	Name     string      `json:"name"`
	Artists  []rawArtist `json:"artists"`
	Album    rawAlbum    `json:"album"`
	Duration int         `json:"duration_ms"`
	TrackNum int         `json:"track_number"`
	DiscNum  int         `json:"disc_number"`
}

type rawPlaylistResponse struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Images      []rawImage      `json:"images"`
	Owner       rawPlaylistOwner `json:"owner"`
	Tracks      struct {
		Total int `json:"total"`
	} `json:"tracks"`
}

type rawPlaylistOwner struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

func ParsePlaylistID(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	if strings.HasPrefix(input, "spotify:playlist:") {
		return strings.TrimPrefix(input, "spotify:playlist:")
	}

	if strings.Contains(input, "open.spotify.com/playlist/") {
		parts := strings.Split(input, "/playlist/")
		if len(parts) == 2 {
			id := parts[1]
			if idx := strings.Index(id, "?"); idx != -1 {
				id = id[:idx]
			}
			return id
		}
	}

	if strings.Contains(input, "/playlist/") {
		parts := strings.Split(input, "/playlist/")
		if len(parts) == 2 {
			id := parts[1]
			if idx := strings.Index(id, "?"); idx != -1 {
				id = id[:idx]
			}
			return id
		}
	}

	return input
}

func (s *Service) GetPlaylistByID(ctx context.Context, playlistID string) (*domain.Playlist, error) {
	if _, httpClient := s.clients(); httpClient != nil {
		url := fmt.Sprintf("https://api.spotify.com/v1/playlists/%s", playlistID)
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := httpClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var data rawPlaylistResponse
				if err := json.NewDecoder(resp.Body).Decode(&data); err == nil {
					pl := &domain.Playlist{
						ID:          data.ID,
						Name:        data.Name,
						Description: data.Description,
						TrackCount:  data.Tracks.Total,
						Owner:       data.Owner.DisplayName,
						OwnerID:     data.Owner.ID,
					}
					if len(data.Images) > 0 {
						pl.ImageURL = data.Images[0].URL
					}
					return pl, nil
				}
			}
		}
	}
	return s.fetchPlaylistAnon(ctx, playlistID)
}

func (s *Service) SearchTracks(ctx context.Context, query string) ([]domain.Song, error) {
	_, httpClient := s.clients()
	if httpClient == nil {
		return nil, fmt.Errorf("spotify client not initialized")
	}

	url := fmt.Sprintf("https://api.spotify.com/v1/search?q=%s&type=track&limit=10", url.QueryEscape(query))
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spotify search: HTTP %d", resp.StatusCode)
	}

	var data rawSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("parse search response: %v", err)
	}

		result := make([]domain.Song, 0, len(data.Tracks.Items))
		for _, item := range data.Tracks.Items {
			song := domain.Song{
				ID:    item.ID,
				Title: item.Name,
			}
			if len(item.Artists) > 0 {
				song.Artist = item.Artists[0].Name
				parts := make([]string, len(item.Artists))
				for i, a := range item.Artists {
					parts[i] = a.Name
				}
				song.AlbumArtist = strings.Join(parts, ", ")
			}
			if item.Album.Name != "" {
				song.Album = item.Album.Name
				if len(item.Album.Images) > 0 {
					song.AlbumArt = item.Album.Images[0].URL
				}
				song.Year = parseYear(item.Album.ReleaseDate)
			}
			if item.Duration > 0 {
				song.Duration = item.Duration / 1000
			}
			if item.TrackNum > 0 {
				song.TrackNum = item.TrackNum
			}
			if item.DiscNum > 0 {
				song.DiscNum = item.DiscNum
			}
			result = append(result, song)
		}

	return result, nil
}

func (s *Service) GetPlaylistTracks(ctx context.Context, playlistID string) ([]domain.Song, error) {
	if client, _ := s.clients(); client == nil {
		return nil, fmt.Errorf("spotify client not initialized")
	}

	if s.db != nil {
		cached, stale, err := s.db.GetCachedTracksStale(playlistID)
		if err == nil && cached != nil {
			if stale {
				go s.refreshTracks(playlistID)
			}
			return cached, nil
		}
	}

	return s.fetchTracks(ctx, playlistID)
}

func (s *Service) RefreshPlaylistTracks(ctx context.Context, playlistID string) ([]domain.Song, error) {
	if client, _ := s.clients(); client == nil {
		return nil, fmt.Errorf("spotify client not initialized")
	}
	return s.fetchTracks(ctx, playlistID)
}

func (s *Service) refreshTracks(playlistID string) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	_, err := s.fetchTracks(ctx, playlistID)
	if err != nil {
		logs.Error("background tracks refresh for %s: %v", playlistID, err)
	}
}

func (s *Service) fetchTracks(ctx context.Context, playlistID string) ([]domain.Song, error) {
	var result []domain.Song
	var err error

	_, httpClient := s.clients()
	if httpClient != nil {
		result, err = s.fetchTracksOAuth(ctx, httpClient, playlistID)
	}

	if err != nil || httpClient == nil {
		result, err = s.fetchTracksAnon(ctx, playlistID)
	}

	if err == nil && s.db != nil {
		if err := s.db.CacheTracks(playlistID, result); err != nil {
			logs.Error("failed to cache tracks for %s: %v", playlistID, err)
		}
	}

	return result, err
}

func (s *Service) fetchTracksOAuth(ctx context.Context, httpClient *http.Client, playlistID string) ([]domain.Song, error) {
	url := fmt.Sprintf("https://api.spotify.com/v1/playlists/%s/items?limit=50", playlistID)

	var result []domain.Song
	for {
		req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("request failed: %v", err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("spotify: HTTP %d", resp.StatusCode)
		}

		var data rawPlaylistItemsResponse
		if err := json.Unmarshal(body, &data); err != nil {
			return nil, fmt.Errorf("parse response: %v", err)
		}

		for _, item := range data.Items {
			song := domain.Song{
				ID:         item.Track.ID,
				Title:      item.Track.Name,
				PlaylistID: playlistID,
			}
			if len(item.Track.Artists) > 0 {
				song.Artist = item.Track.Artists[0].Name
				parts := make([]string, len(item.Track.Artists))
				for i, a := range item.Track.Artists {
					parts[i] = a.Name
				}
				song.AlbumArtist = strings.Join(parts, ", ")
			}
			if item.Track.Album.Name != "" {
				song.Album = item.Track.Album.Name
				if len(item.Track.Album.Images) > 0 {
					song.AlbumArt = item.Track.Album.Images[0].URL
				}
				song.Year = parseYear(item.Track.Album.ReleaseDate)
			}
			if item.Track.Duration > 0 {
				song.Duration = item.Track.Duration / 1000
			}
			if item.Track.TrackNum > 0 {
				song.TrackNum = item.Track.TrackNum
			}
			if item.Track.DiscNum > 0 {
				song.DiscNum = item.Track.DiscNum
			}
			result = append(result, song)
		}

		if data.Next == "" {
			break
		}
		url = data.Next
	}

	if s.db != nil {
		if err := s.db.CacheTracks(playlistID, result); err != nil {
			logs.Error("failed to cache tracks for %s: %v", playlistID, err)
		}
	}

	return result, nil
}

func parseYear(date string) int {
	if len(date) >= 4 {
		y, err := strconv.Atoi(date[:4])
		if err == nil && y > 0 {
			return y
		}
	}
	return 0
}
