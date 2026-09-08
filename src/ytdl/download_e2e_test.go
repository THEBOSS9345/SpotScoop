package ytdl

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"spotscoop/src/domain"
	"spotscoop/src/infra/config"
)

// TestDownloadEndToEnd exercises the real Search+Download path against YouTube,
// which is the only way to catch extraction breakages like the PO Token
// enforcement that made every download fail with HTTP 403.
//
// It hits the network, so it runs only when SPOTSCOOP_E2E=1.
func TestDownloadEndToEnd(t *testing.T) {
	if os.Getenv("SPOTSCOOP_E2E") != "1" {
		t.Skip("set SPOTSCOOP_E2E=1 to run the network end-to-end test")
	}

	outDir := t.TempDir()
	svc := New(outDir, 4, config.YoutubeConfig{})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	song := domain.Song{
		Title:    "1-800-273-8255",
		Artist:   "Logic",
		Album:    "Everybody",
		Duration: 250,
	}

	results, err := svc.Search(ctx, song.Artist+" - "+song.Title)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("search returned no results")
	}

	var lastStatus domain.DownloadStatus
	path, err := svc.Download(ctx, results[0], song, func(p domain.DownloadProgress) {
		lastStatus = p.Status
	})
	if err != nil {
		t.Fatalf("download: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected mp3 at %s: %v", path, err)
	}
	if info.Size() < 512*1024 {
		t.Fatalf("mp3 suspiciously small: %d bytes", info.Size())
	}
	if filepath.Ext(path) != ".mp3" {
		t.Fatalf("expected .mp3, got %s", path)
	}
	if lastStatus != domain.DownloadComplete {
		t.Fatalf("expected terminal status complete, got %q", lastStatus)
	}

	// The intermediate files must not survive a successful run.
	for _, ext := range []string{".m4a", ".jpg"} {
		stray := path[:len(path)-len(".mp3")] + ext
		if _, err := os.Stat(stray); err == nil {
			t.Errorf("leftover temp file: %s", stray)
		}
	}

	t.Logf("downloaded %s (%d bytes)", path, info.Size())
}
