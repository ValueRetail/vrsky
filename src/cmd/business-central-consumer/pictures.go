package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"unicode"

	"github.com/ValueRetail/vrsky/pkg/agentproto"
	"github.com/ValueRetail/vrsky/pkg/envelope"
	"github.com/ValueRetail/vrsky/pkg/oauthcc"
)

// Pictures (#281): a consumer node with `pictures: true` publishes its records
// exactly as without it, and after each page one message per picture of that
// page's records — the image file, with BC's content type, the record's
// identity and a file name in the metadata. The file and remote-agent
// destinations write it under that name; the transforms pass it through.
// Only a picture that is new, or whose id changed, is downloaded and sent.
// Business Central API v2.0 exposes pictures on these entities only.
var pictureEntities = map[string]bool{
	"items": true, "customers": true, "vendors": true, "employees": true, "contacts": true,
}

// maxBufferedPicture bounds a picture read into memory when no streaming
// store is configured. Business Central keeps pictures as media blobs; a
// product photo is kilobytes to a few megabytes.
const maxBufferedPicture = 64 << 20

// defaultInlineMax matches the SDK's claim-check threshold when Configure did
// not supply one (tests, or a worker without a payload store).
const defaultInlineMax = 256 << 10

func (cfg *BCConfig) validatePictures() error {
	if cfg.Pictures && !pictureEntities[cfg.effectiveEntity()] {
		return fmt.Errorf("pictures is only available for items, customers, vendors, employees and contacts (entity is %q)",
			cfg.effectiveEntity())
	}
	return nil
}

type pictureRecord struct {
	ID          string `json:"id"`
	Number      string `json:"number"`
	DisplayName string `json:"displayName"`
}

type pictureInfo struct {
	ID          string `json:"id"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	ContentType string `json:"contentType"`
	ReadLink    string `json:"pictureContent@odata.mediaReadLink"`
}

type pictureCounts struct{ sent, none, unchanged int }

// publishPictures fetches and publishes the picture of each record on a page.
// Any failure other than "this record has no picture" is returned, so the
// caller does not advance the watermark and the next poll retries.
func (c *bcConsumer) publishPictures(ctx context.Context, connID, tenantID string, cfg *BCConfig,
	tok *oauthcc.Client, records []json.RawMessage, logger *slog.Logger) (pictureCounts, error) {
	var n pictureCounts
	if cfg.pictureSeen == nil {
		cfg.pictureSeen = map[string]string{}
	}
	for _, raw := range records {
		var rec pictureRecord
		if err := json.Unmarshal(raw, &rec); err != nil || rec.ID == "" {
			logger.Warn("Business Central record without an id; no picture fetched", "entity", cfg.effectiveEntity())
			continue
		}
		recordURL := fmt.Sprintf("%s/%s(%s)", cfg.companyURL(), cfg.effectiveEntity(), rec.ID)

		body, status, err := c.getStatus(ctx, tok, recordURL+"/picture")
		if err != nil {
			return n, fmt.Errorf("picture of %s %s: %w", cfg.effectiveEntity(), rec.ID, err)
		}
		if status == http.StatusNotFound {
			n.none++
			continue
		}
		var pic pictureInfo
		if err := json.Unmarshal(body, &pic); err != nil {
			return n, fmt.Errorf("parse picture of %s %s: %w", cfg.effectiveEntity(), rec.ID, err)
		}
		if pic.ContentType == "" {
			n.none++ // BC answers an empty picture object for a record without one
			continue
		}
		// A replaced picture is a new media object with a new id; an unchanged
		// one is not downloaded again by this poller.
		if pic.ID != "" && cfg.pictureSeen[rec.ID] == pic.ID {
			n.unchanged++
			continue
		}

		sent, err := c.publishPicture(ctx, connID, tenantID, cfg, tok, rec, pic, recordURL)
		if err != nil {
			return n, fmt.Errorf("picture of %s %s: %w", cfg.effectiveEntity(), rec.ID, err)
		}
		if !sent {
			n.none++
			continue
		}
		if pic.ID != "" {
			cfg.pictureSeen[rec.ID] = pic.ID
		}
		n.sent++
	}
	return n, nil
}

// publishPicture downloads one picture's content and publishes it. It reports
// false when the content vanished between the metadata and content requests.
func (c *bcConsumer) publishPicture(ctx context.Context, connID, tenantID string, cfg *BCConfig,
	tok *oauthcc.Client, rec pictureRecord, pic pictureInfo, recordURL string) (bool, error) {
	contentURL := cfg.pictureContentURL(pic, recordURL)
	resp, err := c.open(ctx, tok, contentURL)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return false, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return false, fmt.Errorf("business central %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}

	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = pic.ContentType
	env.Source = "business-central-consumer"
	env.StepHistory = []string{"business-central-consumer"}
	meta := map[string]interface{}{
		"entity":     cfg.effectiveEntity(),
		"record_id":  rec.ID,
		"picture_id": pic.ID,
		"width":      pic.Width,
		"height":     pic.Height,
		"filename":   pictureFilename(rec, pic.ContentType),
	}
	if rec.Number != "" {
		meta["number"] = rec.Number
	}
	if rec.DisplayName != "" {
		meta["display_name"] = rec.DisplayName
	}
	env.Metadata = meta

	inlineMax := c.inlineMax
	if inlineMax <= 0 {
		inlineMax = defaultInlineMax
	}
	// Large and a store to stream into: never hold the image in memory.
	if c.publishStream != nil && resp.ContentLength > int64(inlineMax) {
		return true, c.publishStream(ctx, env, resp.Body)
	}
	head, err := io.ReadAll(io.LimitReader(resp.Body, int64(inlineMax)+1))
	if err != nil {
		return false, fmt.Errorf("read picture content: %w", err)
	}
	// Length not announced but larger than inline: stream the rest after
	// what was already read.
	if len(head) > inlineMax && c.publishStream != nil {
		return true, c.publishStream(ctx, env, io.MultiReader(bytes.NewReader(head), resp.Body))
	}
	if len(head) > inlineMax {
		rest, err := io.ReadAll(io.LimitReader(resp.Body, maxBufferedPicture-int64(len(head))+1))
		if err != nil {
			return false, fmt.Errorf("read picture content: %w", err)
		}
		head = append(head, rest...)
		if len(head) > maxBufferedPicture {
			return false, fmt.Errorf("picture is larger than %d MiB and this worker has no payload store to stream it into",
				maxBufferedPicture>>20)
		}
	}
	env.Payload = head
	env.PayloadSize = int64(len(head))
	return true, c.publish(ctx, env) // the SDK offloads anything over the inline limit
}

// pictureContentURL is the picture's content link on the configured API host.
// BC reports the media link with its own view of its hostname — on-prem that
// is often an internal name the worker cannot reach — so only the path and
// query are taken from it.
func (cfg *BCConfig) pictureContentURL(pic pictureInfo, recordURL string) string {
	fallback := fmt.Sprintf("%s/picture(%s)/content", recordURL, pic.ID)
	if pic.ReadLink == "" {
		return fallback
	}
	link, err := url.Parse(pic.ReadLink)
	if err != nil {
		return fallback
	}
	base, err := url.Parse(cfg.companyURL())
	if err != nil || !link.IsAbs() {
		return fallback
	}
	link.Scheme, link.Host = base.Scheme, base.Host
	return link.String()
}

// pictureFilename names the file a destination writes: the record's number
// (the item number a person recognises), else its id, plus an extension for
// the content type. Anything a Windows or remote-agent file name cannot hold
// is replaced, and a name that is still unusable falls back to the id.
func pictureFilename(rec pictureRecord, contentType string) string {
	ext := pictureExt(contentType)
	if rec.Number != "" {
		name := sanitizeFilename(rec.Number) + "." + ext
		if agentproto.ValidFilename(name) == nil {
			return name
		}
	}
	return sanitizeFilename(rec.ID) + "." + ext
}

func pictureExt(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(contentType))
	}
	switch mt {
	case "image/jpeg", "image/jpg", "image/pjpeg":
		return "jpg"
	case "image/png":
		return "png"
	case "image/gif":
		return "gif"
	case "image/bmp", "image/x-ms-bmp":
		return "bmp"
	case "image/tiff":
		return "tif"
	case "image/webp":
		return "webp"
	}
	if exts, _ := mime.ExtensionsByType(mt); len(exts) > 0 {
		return strings.TrimPrefix(exts[0], ".")
	}
	return "bin"
}

func sanitizeFilename(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || strings.ContainsRune(`/\<>:"|?*`, r) {
			return '_'
		}
		return r
	}, strings.TrimSpace(s))
	// Windows drops trailing dots and spaces, so two names could collide.
	s = strings.TrimRight(s, ". ")
	if s == "" {
		return "picture"
	}
	return s
}

// getStatus is get for requests where a 404 is an answer, not a failure.
func (c *bcConsumer) getStatus(ctx context.Context, tok *oauthcc.Client, fullURL string) ([]byte, int, error) {
	resp, err := c.open(ctx, tok, fullURL)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusNotFound {
		return nil, resp.StatusCode, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("business central %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return body, resp.StatusCode, nil
}

// open sends an authenticated GET and returns the response for the caller to
// read and close.
func (c *bcConsumer) open(ctx context.Context, tok *oauthcc.Client, fullURL string) (*http.Response, error) {
	access, err := tok.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire token: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fullURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+access)
	return c.httpClient.Do(req)
}
