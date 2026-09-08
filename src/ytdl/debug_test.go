package ytdl

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lrstanley/go-ytdlp"
)

// TestDirectDownload is a scratch diagnostic that prints environment details
// and asserts nothing. It hits the network, so it is opt-in; see
// TestDownloadEndToEnd for the actual download regression test.
func TestDirectDownload(t *testing.T) {
	if os.Getenv("SPOTSCOOP_E2E") != "1" {
		t.Skip("set SPOTSCOOP_E2E=1 to run the yt-dlp diagnostic")
	}
	os.Setenv("YTDLP_DEBUG", "true")

	url := "https://www.youtube.com/watch?v=XoiOOiuH8iI"
	dir, _ := os.Getwd()
	outPath := filepath.Join(dir, "test_debug.m4a")

	fmt.Println("=== Testing go-ytdlp download ===")
	fmt.Printf("URL: %s\n", url)
	fmt.Printf("Output: %s\n", outPath)
	fmt.Println()

	dl := ytdlp.New().
		NoPlaylist().
		NoWarnings().
		ExtractAudio().
		AudioFormat("m4a").
		AudioQuality("0").
		AgeLimit(99).
		Output(outPath)

	cmd := dl.BuildCommand(context.Background(), url)
	fmt.Printf("Executable: %s\n", cmd.Path)
	fmt.Printf("Args: %v\n", cmd.Args)

	r, err := dl.Run(context.Background(), url)
	if err != nil {
		t.Logf("Run failed: %v", err)
	}
	if r != nil {
		fmt.Printf("Exit code: %d\n", r.ExitCode)
		fmt.Printf("Stdout: %s\n", strings.TrimSpace(r.Stdout))
		fmt.Printf("Stderr: %s\n", strings.TrimSpace(r.Stderr))
	}
	os.Remove(outPath)

	fmt.Println()
	fmt.Println("=== Testing direct yt-dlp exec ===")
	out, _ := exec.CommandContext(context.Background(), "yt-dlp", "--version").Output()
	fmt.Printf("System yt-dlp version: %s", string(out))
	fmt.Printf("go-ytdlp bundled version: %s\n", ytdlp.Version)

	path, _ := exec.LookPath("yt-dlp")
	fmt.Printf("yt-dlp PATH location: %s\n", path)
}
