package ytdl

import (
	"testing"

	"spotscoop/src/infra/config"
)

// TestDefaultPlayerClientIsTokenFree pins the default to a client that does not
// require a GVS PO Token. Switching it to a token-requiring client (web,
// web_safari, mweb, tv_simply, ios, android) makes every download fail with
// HTTP 403 or "requested format is not available".
func TestDefaultPlayerClientIsTokenFree(t *testing.T) {
	if config.DefaultPlayerClient != "web_embedded" {
		t.Fatalf("default player client changed to %q; verify it needs no PO Token before updating this test",
			config.DefaultPlayerClient)
	}

	if got := resolvePlayerClient(config.YoutubeConfig{}); got != "web_embedded" {
		t.Fatalf("empty config should resolve to web_embedded, got %q", got)
	}
}

func TestBuildExtractorArgs(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.YoutubeConfig
		want string
	}{
		{
			name: "empty config uses the default client",
			cfg:  config.YoutubeConfig{},
			want: "youtube:player_client=web_embedded",
		},
		{
			name: "explicit client overrides the default",
			cfg:  config.YoutubeConfig{PlayerClient: "tv"},
			want: "youtube:player_client=tv",
		},
		{
			name: "po token is joined with a semicolon",
			cfg:  config.YoutubeConfig{PoToken: "web.gvs+AbC123"},
			want: "youtube:player_client=web_embedded;po_token=web.gvs+AbC123",
		},
		{
			name: "client and po token together",
			cfg:  config.YoutubeConfig{PlayerClient: "mweb", PoToken: "mweb.gvs+XYZ"},
			want: "youtube:player_client=mweb;po_token=mweb.gvs+XYZ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := buildExtractorArgs(tt.cfg); got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
