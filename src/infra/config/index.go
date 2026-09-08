package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type SpotifyConfig struct {
	ClientId     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
}

type YoutubeConfig struct {
	Cookies string `json:"cookies"`

	// PlayerClient overrides the yt-dlp InnerTube client used for extraction.
	// Empty means DefaultPlayerClient.
	PlayerClient string `json:"playerClient"`

	// PoToken supplies a Proof-of-Origin token when YouTube demands one, in
	// yt-dlp's "CLIENT.CONTEXT+TOKEN" form (e.g. "web.gvs+AbC123..."). Only
	// needed if PlayerClient is changed to a client that requires one, or if
	// YouTube starts enforcing tokens on the default client. See
	// https://github.com/yt-dlp/yt-dlp/wiki/PO-Token-Guide
	PoToken string `json:"poToken"`

	// DirectDownload re-enables the concurrent CDN downloader. It is off by
	// default: YouTube enforces PO Tokens on the media CDN, so a URL fetched
	// outside yt-dlp's own session is rejected with 403 and the download falls
	// back anyway, costing a wasted round trip per track.
	DirectDownload bool `json:"directDownload"`
}

// DefaultPlayerClient is the InnerTube client yt-dlp extracts with. Most
// clients now require a GVS PO Token and return 403 (or expose no formats)
// without one; web_embedded still serves audio unauthenticated.
const DefaultPlayerClient = "web_embedded"

type Config struct {
	Spotify                SpotifyConfig `json:"spotify"`
	OutputDir              string        `json:"outputDir"`
	MaxConcurrentDownloads int           `json:"maxConcurrentDownloads"`
	MaxDownloadThreads     int           `json:"maxDownloadThreads"`
	Debug                  bool          `json:"debug"`
	Youtube                YoutubeConfig `json:"youtube"`
}

const DefaultOutputDir = "downloads"

func Init() (*Config, error) {
	c, err := Read()
	if err != nil {
		return nil, err
	}

	if c.OutputDir == "" {
		c.OutputDir = DefaultOutputDir
	}

	c.OutputDir = filepath.Clean(filepath.FromSlash(c.OutputDir))

	if err := os.MkdirAll(c.OutputDir, 0755); err != nil {
		return nil, err
	}

	validateConfig, err := Validate(c)
	if err != nil {
		return nil, err
	}

	return validateConfig, nil
}

func Read() (*Config, error) {
	fileBytes, err := os.ReadFile("config.json")

	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			err := Write(&Config{})

			if err != nil {
				return nil, errors.New("Failed to create config file: " + err.Error())
			}

			return &Config{}, nil
		}
		return nil, err
	}

	var config Config
	if err := json.Unmarshal(fileBytes, &config); err != nil {
		return nil, fmt.Errorf("invalid config.json: %w\nHint: Windows paths need double backslashes (e.g. \"D:\\\\downloads\") or forward slashes (\"D:/downloads\")", err)
	}

	return &config, nil
}

func Write(config *Config) error {
	indent, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile("config.json", indent, 0644); err != nil {
		return err
	}

	return nil
}
