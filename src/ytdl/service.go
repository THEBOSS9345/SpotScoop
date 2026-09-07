package ytdl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"spotscoop/src/domain"
	"spotscoop/src/infra/config"
	"spotscoop/src/infra/logs"

	"github.com/lrstanley/go-ytdlp"
)

var cdnHTTPClient = &http.Client{Timeout: 60 * time.Second}

// errCDNForbidden signals that the extracted CDN URL is not usable by this
// client, so the caller should fall back to yt-dlp's own downloader.
var errCDNForbidden = errors.New("CDN returned 403 Forbidden")

// doWithRetry retries transient/rate-limit failures against the YouTube CDN
// with exponential backoff, honoring Retry-After when the CDN sends one.
//
// 403 is deliberately not retried: the CDN binds the extracted URL to the
// session yt-dlp used to sign it, so a 403 is a permanent rejection of this
// client rather than throttling, and retrying only delays the fallback.
func doWithRetry(req *http.Request, maxAttempts int) (*http.Response, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := retryBackoff(attempt)
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(delay):
			}
		}

		resp, err := cdnHTTPClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}

		if resp.StatusCode == http.StatusForbidden {
			resp.Body.Close()
			return nil, errCDNForbidden
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("HTTP %d", resp.StatusCode)
			if retryAfter := parseRetryAfter(resp.Header.Get("Retry-After")); retryAfter > 0 {
				resp.Body.Close()
				select {
				case <-req.Context().Done():
					return nil, req.Context().Err()
				case <-time.After(retryAfter):
				}
				continue
			}
			resp.Body.Close()
			continue
		}

		return resp, nil
	}
	return nil, fmt.Errorf("request failed after %d attempts: %w", maxAttempts, lastErr)
}

func retryBackoff(attempt int) time.Duration {
	base := time.Duration(1<<uint(attempt-1)) * time.Second
	jitter := time.Duration(rand.Int63n(int64(500 * time.Millisecond)))
	if base > 20*time.Second {
		base = 20 * time.Second
	}
	return base + jitter
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

type SearchResult struct {
	Title     string `json:"title"`
	VideoID   string `json:"id"`
	Duration  int    `json:"duration"`
	Thumbnail string `json:"thumbnail"`
	Uploader  string `json:"uploader"`
	URL       string `json:"webpage_url"`
}

type InstallPaths struct {
	YtDlp   string
	FFmpeg  string
	FFprobe string
	Bun     string
}

type Service struct {
	outputDir          string
	dl                 *ytdlp.Command
	installPaths       InstallPaths
	maxDownloadThreads int
	ytCfg              config.YoutubeConfig
}

func New(outputDir string, maxDownloadThreads int, ytCfg config.YoutubeConfig) *Service {
	logs.Info("Checking and preparing media environments...")

	installPaths, err := EnsureEnvironment()
	if err != nil {
		logs.Error("Environment configuration error: %v", err)
	}

	logs.Info("Environment successfully validated! Ready to download videos.")

	dl := ytdlp.New().
		NoPlaylist().
		NoWarnings().
		JsRuntimes("bun:" + installPaths.Bun).
		SleepRequests(1).
		Retries("10").
		ExtractorRetries("5")

	if ytCfg.Cookies != "" {
		if _, err := os.Stat(ytCfg.Cookies); err == nil {
			dl = dl.Cookies(ytCfg.Cookies)
		} else {
			logs.Warning("cookies file not found: %s", ytCfg.Cookies)
		}
	}

	if err == nil {
		dl = dl.SetExecutable(installPaths.YtDlp)
		dl = dl.FFmpegLocation(filepath.Dir(installPaths.FFmpeg))
	}

	return &Service{
		outputDir:          outputDir,
		dl:                 dl,
		installPaths:       installPaths,
		maxDownloadThreads: maxDownloadThreads,
		ytCfg:              ytCfg,
	}
}

func EnsureEnvironment() (InstallPaths, error) {
	ytdlpResult, err := ytdlp.Install(context.Background(), &ytdlp.InstallOptions{
		DisableSystem: true,
	})
	if err != nil {
		return InstallPaths{}, fmt.Errorf("yt-dlp installation failed: %w", err)
	}

	bunResult, err := ytdlp.InstallBun(context.Background(), &ytdlp.InstallBunOptions{
		DisableSystem: true,
	})

	if err != nil {
		return InstallPaths{}, fmt.Errorf("bun installation failed: %w", err)
	}

	ffmpegResult, err := ytdlp.InstallFFmpeg(context.Background(), &ytdlp.InstallFFmpegOptions{
		DisableSystem: true,
	})
	if err != nil {
		return InstallPaths{}, fmt.Errorf("ffmpeg installation failed: %w", err)
	}

	ffprobeResult, err := ytdlp.InstallFFprobe(context.Background(), &ytdlp.InstallFFmpegOptions{
		DisableSystem: true,
	})
	if err != nil {
		return InstallPaths{}, fmt.Errorf("ffprobe installation failed: %w", err)
	}

	return InstallPaths{
		YtDlp:   ytdlpResult.Executable,
		FFmpeg:  ffmpegResult.Executable,
		FFprobe: ffprobeResult.Executable,
		Bun:     bunResult.Executable,
	}, nil
}

func (s *Service) Search(ctx context.Context, query string) ([]SearchResult, error) {
	searchQuery := fmt.Sprintf("ytsearch3:%s", query)

	dl := s.dl.Clone().
		FlatPlaylist().
		Quiet().
		Print("%(id)s\t%(title)s\t%(duration)s\t%(thumbnail)s\t%(uploader)s\t%(webpage_url)s")

	result, err := dl.Run(ctx, searchQuery)
	if err != nil {
		return nil, fmt.Errorf("yt-dlp search failed: %w", err)
	}

	results := make([]SearchResult, 0, 3)
	scanner := bufio.NewScanner(strings.NewReader(result.Stdout))

	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "\t", 6)
		if len(parts) < 6 {
			continue
		}

		duration := 0
		if parts[2] != "" {
			if d, err := strconv.Atoi(parts[2]); err == nil {
				duration = d
			}
		}

		results = append(results, SearchResult{
			VideoID:   parts[0],
			Title:     parts[1],
			Duration:  duration,
			Thumbnail: parts[3],
			Uploader:  parts[4],
			URL:       parts[5],
		})
	}

	return results, nil
}

func (s *Service) Download(
	ctx context.Context,
	result SearchResult,
	song domain.Song,
	onProgress func(domain.DownloadProgress),
) (string, error) {

	onProgress(domain.DownloadProgress{Status: domain.DownloadDownloading, Progress: 10})

	safeArtist := safeFilename(song.Artist)
	safeTitle := safeFilename(song.Title)
	filename := fmt.Sprintf("%s - %s", safeArtist, safeTitle)

	os.Remove(filepath.Join(s.outputDir, filename+".m4a"))
	os.Remove(filepath.Join(s.outputDir, filename+".mp3"))
	os.Remove(filepath.Join(s.outputDir, filename+".jpg"))

	onProgress(domain.DownloadProgress{Status: domain.DownloadDownloading, Progress: 25})

	if err := s.downloadWithYtDlp(ctx, result, filename, onProgress); err != nil {
		logs.Error("yt-dlp download failed for %s: %v", song.Title, err)
		return "", err
	}

	onProgress(domain.DownloadProgress{Status: domain.DownloadDownloading, Progress: 55})

	hasThumb := s.fetchThumbnail(result, filename, song.AlbumArt)

	onProgress(domain.DownloadProgress{Status: domain.DownloadDownloading, Progress: 70})

	m4aPath := filepath.Join(s.outputDir, filename+".m4a")
	mp3Path := filepath.Join(s.outputDir, filename+".mp3")
	thumbPath := filepath.Join(s.outputDir, filename+".jpg")

	ffmpegArgs := buildFFmpegArgs(m4aPath, mp3Path, thumbPath, hasThumb, song, result.URL)
	cmd := exec.CommandContext(ctx, s.installPaths.FFmpeg, ffmpegArgs...)
	if output, err := cmd.CombinedOutput(); err != nil {
		os.Remove(m4aPath)
		os.Remove(thumbPath)
		logs.Error("ffmpeg conversion failed for %s: %v", song.Title, err)
		return "", fmt.Errorf("ffmpeg conversion failed: %w\n%s", err, string(output))
	}

	os.Remove(m4aPath)
	os.Remove(thumbPath)

	onProgress(domain.DownloadProgress{Status: domain.DownloadComplete, Progress: 100})

	logs.Success("Downloaded: %s -> %s", song.Title, mp3Path)
	return mp3Path, nil
}

func buildFFmpegArgs(m4aPath, mp3Path, thumbPath string, hasThumb bool, song domain.Song, sourceURL string) []string {
	args := []string{"-i", m4aPath}

	if hasThumb {
		args = append(args,
			"-i", thumbPath,
			"-map", "0:a:0",
			"-map", "1:v:0",
			"-c:v", "mjpeg",
			"-vf", "crop='min(iw,ih)':'min(iw,ih)',scale=800:800",
			"-id3v2_version", "3",
			"-metadata:s:v", "title=Album cover",
			"-metadata:s:v", "comment=Cover (front)",
			"-disposition:v", "attached_pic",
		)
	} else {
		args = append(args, "-map", "0:a:0")
	}

	metas := []string{
		fmt.Sprintf("title=%s", song.Title),
		fmt.Sprintf("artist=%s", song.Artist),
		fmt.Sprintf("album=%s", song.Album),
		fmt.Sprintf("comment=%s", sourceURL),
	}
	if song.AlbumArtist != "" {
		metas = append(metas, fmt.Sprintf("album_artist=%s", song.AlbumArtist))
	}
	if song.TrackNum > 0 {
		metas = append(metas, fmt.Sprintf("track=%d/%d", song.TrackNum, song.TrackNum))
	}
	if song.DiscNum > 0 {
		metas = append(metas, fmt.Sprintf("disc=%d/%d", song.DiscNum, song.DiscNum))
	}
	if song.Year > 0 {
		metas = append(metas, fmt.Sprintf("date=%d", song.Year))
	}
	args = append(args, "-c:a", "libmp3lame", "-q:a", "2")
	for _, m := range metas {
		args = append(args, "-metadata", m)
	}
	args = append(args, "-y", mp3Path)
	return args
}

func setFirefoxHeaders(req *http.Request) {
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0")
	req.Header.Set("Accept", "video/webm,video/ogg,video/*;q=0.9,application/ogg;q=0.7,audio/*;q=0.6,*/*;q=0.5")
	req.Header.Set("Accept-Language", "en-US,en;q=0.5")
	req.Header.Set("Accept-Encoding", "identity;q=1, *;q=0")
	req.Header.Set("Referer", "https://www.youtube.com/")
	req.Header.Set("Origin", "https://www.youtube.com")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Sec-Fetch-Dest", "video")
	req.Header.Set("Sec-Fetch-Mode", "no-cors")
	req.Header.Set("Sec-Fetch-Site", "same-site")
	req.Header.Set("DNT", "1")
}

func (s *Service) downloadChunk(ctx context.Context, url string, f *os.File, start, end int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("chunk request: %w", err)
	}
	req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))
	setFirefoxHeaders(req)

	resp, err := doWithRetry(req, 5)
	if err != nil {
		return fmt.Errorf("chunk request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("chunk unexpected status: %s", resp.Status)
	}

	buf := make([]byte, 256*1024)
	offset := start
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.WriteAt(buf[:n], offset); werr != nil {
				return fmt.Errorf("chunk write: %w", werr)
			}
			offset += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("chunk read: %w", rerr)
		}
	}
	return nil
}

func (s *Service) downloadWithYtDlp(ctx context.Context, result SearchResult, filename string, onProgress func(domain.DownloadProgress)) error {
	err := s.downloadViaCDN(ctx, result, filename, onProgress)
	if errors.Is(err, errCDNForbidden) {
		logs.Warning("CDN rejected direct download for %s, falling back to yt-dlp", result.Title)
		return s.downloadNative(ctx, result, filename, onProgress)
	}
	return err
}

// downloadNative lets yt-dlp fetch the audio itself. It is slower than the
// concurrent CDN path but survives the cases where the CDN refuses a URL
// fetched outside yt-dlp's own session.
func (s *Service) downloadNative(ctx context.Context, result SearchResult, filename string, onProgress func(domain.DownloadProgress)) error {
	dl := s.dl.Clone().
		ExtractAudio().
		AudioFormat("m4a").
		AudioQuality("0").
		AgeLimit(99).
		Output(filepath.Join(s.outputDir, filename+".%(ext)s")).
		ProgressFunc(time.Second, func(update ytdlp.ProgressUpdate) {
			pct := int(update.Percent())
			onProgress(domain.DownloadProgress{
				Status:          domain.DownloadDownloading,
				Progress:        25 + pct/5,
				DownloadedBytes: int64(update.DownloadedBytes),
				TotalBytes:      int64(update.TotalBytes),
			})
		})

	if _, err := dl.Run(ctx, result.URL); err != nil {
		return fmt.Errorf("yt-dlp native download failed: %w", err)
	}

	outputPath := filepath.Join(s.outputDir, filename+".m4a")
	if _, err := os.Stat(outputPath); err != nil {
		return fmt.Errorf("yt-dlp reported success but %s is missing: %w", outputPath, err)
	}

	onProgress(domain.DownloadProgress{Status: domain.DownloadDownloading, Progress: 45})
	logs.Debug("Downloaded via yt-dlp to %s", outputPath)
	return nil
}

func (s *Service) downloadViaCDN(ctx context.Context, result SearchResult, filename string, onProgress func(domain.DownloadProgress)) error {
	outputPath := filepath.Join(s.outputDir, filename+".m4a")

	dl := s.dl.Clone().
		ExtractAudio().
		AudioFormat("m4a").
		AudioQuality("0").
		AgeLimit(99).
		GetURL()

	r, err := dl.Run(ctx, result.URL)
	if err != nil {
		return fmt.Errorf("yt-dlp get-url failed: %w", err)
	}

	mediaURL := strings.TrimSpace(r.Stdout)
	if mediaURL == "" {
		return fmt.Errorf("yt-dlp returned empty URL for %s", result.Title)
	}

	logs.Debug("Got media URL: %s", mediaURL)

	headReq, err := http.NewRequestWithContext(ctx, http.MethodHead, mediaURL, nil)
	if err != nil {
		return fmt.Errorf("head request: %w", err)
	}
	setFirefoxHeaders(headReq)

	headResp, err := doWithRetry(headReq, 5)
	if err != nil {
		return fmt.Errorf("head request: %w", err)
	}
	headResp.Body.Close()

	fileSize := headResp.ContentLength
	acceptsRanges := strings.EqualFold(headResp.Header.Get("Accept-Ranges"), "bytes")

	out, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}

	if fileSize <= 0 || !acceptsRanges {
		logs.Debug("Single-threaded download (size: %d, ranges: %v)", fileSize, acceptsRanges)
		err = s.downloadRange(ctx, mediaURL, out, fileSize, onProgress)
		if err != nil {
			out.Close()
			os.Remove(outputPath)
			return err
		}
		out.Close()
		logs.Debug("Downloaded to %s", outputPath)
		return nil
	}

	logs.Debug("Starting concurrent download with %d workers (file size: %d bytes)", s.maxDownloadThreads, fileSize)

	chunkSize := fileSize / int64(s.maxDownloadThreads)
	var downloaded atomic.Int64
	progCancel := make(chan struct{})
	defer close(progCancel)

	go func() {
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				d := downloaded.Load()
				pct := int(d * 100 / fileSize)
				onProgress(domain.DownloadProgress{
					Status:          domain.DownloadDownloading,
					Progress:        25 + pct/5,
					DownloadedBytes: d,
					TotalBytes:      fileSize,
				})
			case <-progCancel:
				onProgress(domain.DownloadProgress{
					Status:          domain.DownloadDownloading,
					Progress:        45,
					DownloadedBytes: fileSize,
					TotalBytes:      fileSize,
				})
				return
			}
		}
	}()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errCh := make(chan error, s.maxDownloadThreads)
	var wg sync.WaitGroup

	for i := range s.maxDownloadThreads {
		start := chunkSize * int64(i)
		end := chunkSize*int64(i+1) - 1
		if i == s.maxDownloadThreads-1 {
			end = fileSize - 1
		}

		wg.Add(1)
		go func(chunkStart, chunkEnd int64) {
			defer wg.Done()
			if err := s.downloadChunk(ctx, mediaURL, out, chunkStart, chunkEnd); err != nil {
				errCh <- err
				cancel()
				return
			}
			downloaded.Add(chunkEnd - chunkStart + 1)
		}(start, end)
	}

	wg.Wait()
	close(errCh)
	out.Close()

	for err := range errCh {
		if err != nil {
			os.Remove(outputPath)
			if errors.Is(err, errCDNForbidden) {
				return err
			}
			return fmt.Errorf("concurrent download: %w", err)
		}
	}

	logs.Debug("Downloaded %d bytes to %s (%d workers)", fileSize, outputPath, s.maxDownloadThreads)
	return nil
}

func (s *Service) downloadRange(ctx context.Context, url string, f *os.File, fileSize int64, onProgress func(domain.DownloadProgress)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("range request: %w", err)
	}
	setFirefoxHeaders(req)

	resp, err := doWithRetry(req, 5)
	if err != nil {
		return fmt.Errorf("range request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("range unexpected status: %s", resp.Status)
	}

	if fileSize <= 0 {
		fileSize = resp.ContentLength
	}

	buf := make([]byte, 256*1024)
	var written int64
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := f.Write(buf[:n]); werr != nil {
				return fmt.Errorf("range write: %w", werr)
			}
			written += int64(n)

			if fileSize > 0 {
				pct := int(written * 100 / fileSize)
				onProgress(domain.DownloadProgress{
					Status:          domain.DownloadDownloading,
					Progress:        25 + pct/5,
					DownloadedBytes: written,
					TotalBytes:      fileSize,
				})
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("range read: %w", rerr)
		}
	}
	onProgress(domain.DownloadProgress{
		Status:          domain.DownloadDownloading,
		Progress:        45,
		DownloadedBytes: fileSize,
		TotalBytes:      fileSize,
	})
	return nil
}

func (s *Service) fetchThumbnail(result SearchResult, filename string, spotifyArtURL string) bool {
	thumbPath := filepath.Join(s.outputDir, filename+".jpg")

	urls := make([]string, 0, 4)

	if spotifyArtURL != "" {
		urls = append(urls, spotifyArtURL)
	}

	if result.VideoID != "" {
		urls = append(urls,
			fmt.Sprintf("https://img.youtube.com/vi/%s/maxresdefault.jpg", result.VideoID),
			fmt.Sprintf("https://img.youtube.com/vi/%s/hqdefault.jpg", result.VideoID),
		)
	}

	if result.Thumbnail != "" && result.Thumbnail != "NA" {
		urls = append(urls, result.Thumbnail)
	}

	thumbClient := &http.Client{Timeout: 15 * time.Second}
	for _, thumbURL := range urls {
		for attempt := range 3 {
			if attempt > 0 {
				time.Sleep(time.Second)
			}

			resp, err := thumbClient.Get(thumbURL)
			if err != nil || resp.StatusCode != http.StatusOK {
				if resp != nil {
					resp.Body.Close()
				}
				continue
			}

			f, err := os.Create(thumbPath)
			if err != nil {
				resp.Body.Close()
				continue
			}

			_, err = io.Copy(f, resp.Body)
			resp.Body.Close()
			f.Close()

			if err != nil {
				os.Remove(thumbPath)
				continue
			}
			return true
		}
	}
	return false
}

func (s *Service) CleanupOrphans() {
	entries, err := os.ReadDir(s.outputDir)

	if err != nil {
		logs.Warning("cleanup: cannot read output dir: %v", err)
		return
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := filepath.Ext(e.Name())
		if ext != ".m4a" && ext != ".jpg" {
			continue
		}
		base := strings.TrimSuffix(e.Name(), ext)
		mp3Path := filepath.Join(s.outputDir, base+".mp3")
		if _, err := os.Stat(mp3Path); os.IsNotExist(err) {
			path := filepath.Join(s.outputDir, e.Name())
			os.Remove(path)
			logs.Info("cleanup: removed orphaned temp file: %s", path)
		}
	}
	logs.Info("cleanup: finished scanning output dir")
}

func safeFilename(name string) string {
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "-",
		"*", "_", "?", "", "<", "",
		">", "", "|", "_", "\"", "'",
	)
	return strings.TrimSpace(replacer.Replace(name))
}
