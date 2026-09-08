package domain

type DownloadStatus string

const (
	DownloadPending     DownloadStatus = "pending"
	DownloadSearching   DownloadStatus = "searching"
	DownloadDownloading DownloadStatus = "downloading"
	DownloadConverting  DownloadStatus = "converting"
	DownloadComplete    DownloadStatus = "complete"
	DownloadFailed      DownloadStatus = "failed"
)

type Download struct {
	ID              string         `json:"id"`
	BatchID         string         `json:"batchId,omitempty"`
	Song            Song           `json:"song"`
	Status          DownloadStatus `json:"status"`
	Progress        int            `json:"progress"`
	DownloadedBytes int64          `json:"downloadedBytes"`
	TotalBytes      int64          `json:"totalBytes"`
	OutputPath      string         `json:"outputPath"`
	Error           string         `json:"error"`
	CreatedAt       int64          `json:"createdAt"`
}

type DownloadProgress struct {
	Status         DownloadStatus
	Progress       int
	DownloadedBytes int64
	TotalBytes     int64
}

type User struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	AvatarURL   string `json:"avatarUrl"`
	Email       string `json:"email"`
}
