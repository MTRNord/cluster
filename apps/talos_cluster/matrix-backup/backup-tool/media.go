// media.go — Matrix media download and S3 storage.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"maunium.net/go/mautrix"
)

// mediaHTTPClient is used for all media downloads; the 5-minute timeout
// prevents a single large file from stalling the entire backup run.
var mediaHTTPClient = &http.Client{Timeout: 5 * time.Minute}

var mediaMsgTypes = map[string]bool{
	"m.image": true,
	"m.file":  true,
	"m.video": true,
	"m.audio": true,
}

// downloadAndStoreMedia downloads a single mxc:// URL and stores it under
// prefix/label/server/mediaID in S3, skipping if already present.
func downloadAndStoreMedia(ctx context.Context, client *mautrix.Client, mxcURL, prefix, label string) error {
	if !strings.HasPrefix(mxcURL, "mxc://") {
		return nil
	}
	rest := mxcURL[len("mxc://"):]
	slashIdx := strings.Index(rest, "/")
	if slashIdx < 0 {
		return nil
	}
	server := rest[:slashIdx]
	mediaID := rest[slashIdx+1:]
	if server == "" || mediaID == "" {
		return nil
	}

	s3Key := prefix + "/" + label + "/" + server + "/" + mediaID
	if s3Exists(ctx, s3Key) {
		return nil
	}

	base := strings.TrimRight(client.HomeserverURL.String(), "/")
	downloadURL := base + "/_matrix/media/v3/download/" +
		url.PathEscape(server) + "/" + url.PathEscape(mediaID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+client.AccessToken)
	resp, err := mediaHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("media download HTTP %d for %s", resp.StatusCode, mxcURL)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/octet-stream"
	}

	finalKey := s3Key
	if ext := extFromContentType(ct); ext != "" {
		finalKey = s3Key + ext
	}
	if err := s3Put(ctx, finalKey, data, ct); err != nil {
		return err
	}
	slog.Info("Stored media", "key", finalKey, "bytes", len(data))
	return nil
}

func extFromContentType(ct string) string {
	if i := strings.Index(ct, ";"); i >= 0 {
		ct = ct[:i]
	}
	switch strings.TrimSpace(ct) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "video/mp4":
		return ".mp4"
	case "video/webm":
		return ".webm"
	case "audio/mpeg":
		return ".mp3"
	case "audio/ogg":
		return ".ogg"
	case "audio/opus":
		return ".opus"
	case "application/pdf":
		return ".pdf"
	}
	return ""
}
