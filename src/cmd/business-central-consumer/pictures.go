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
	"sort"
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

type pictureCounts struct{ sent, none, unchanged, removed, swept int }

func (n *pictureCounts) add(m pictureCounts) {
	n.sent += m.sent
	n.none += m.none
	n.unchanged += m.unchanged
	n.removed += m.removed
	n.swept += m.swept
}

// seenPicture is what the node remembers per record: the picture's id (a
// replaced picture gets a new one) and the record number the file was named
// after, so a removal can be reported even when the record is not in the
// feed any more.
type seenPicture struct {
	PictureID string `json:"picture_id"`
	Number    string `json:"number"`
}

// pictureState is the node's checkpoint state (checkpoint.Checkpoint.State).
type pictureState struct {
	Pictures map[string]seenPicture `json:"pictures"`
}

// pictureState serialises what the node remembers, for the checkpoint row.
func (cfg *BCConfig) pictureState() json.RawMessage {
	if cfg.pictureSeen == nil {
		return nil
	}
	b, err := json.Marshal(pictureState{Pictures: cfg.pictureSeen})
	if err != nil {
		return nil
	}
	return b
}

// loadPictureState reads the remembered pictures back from the checkpoint
// row; a missing row or an unreadable store means "nothing remembered",
// which costs a re-send of every picture, not data.
func (c *bcConsumer) loadPictureState(ctx context.Context, tenantID, connID, nodeID string, logger *slog.Logger) map[string]seenPicture {
	seen := map[string]seenPicture{}
	if c.checkpoints == nil || nodeID == "" {
		return seen
	}
	cp, err := c.checkpoints.Get(ctx, tenantID, connID, nodeID)
	if err != nil {
		logger.Error("read Business Central picture state; every picture will be sent again", "error", err)
		return seen
	}
	if cp == nil || len(cp.State) == 0 {
		return seen
	}
	var st pictureState
	if err := json.Unmarshal(cp.State, &st); err != nil {
		logger.Error("parse Business Central picture state; every picture will be sent again", "error", err)
		return seen
	}
	if st.Pictures != nil {
		seen = st.Pictures
	}
	return seen
}

// publishPictures fetches and publishes the picture of each record on a page.
// Any failure other than "this record has no picture" is returned, so the
// caller does not advance the watermark and the next poll retries.
func (c *bcConsumer) publishPictures(ctx context.Context, connID, tenantID string, cfg *BCConfig,
	tok *oauthcc.Client, records []json.RawMessage, inFeed map[string]bool, logger *slog.Logger) (pictureCounts, error) {
	var n pictureCounts
	if cfg.pictureSeen == nil {
		cfg.pictureSeen = map[string]seenPicture{}
	}
	for _, raw := range records {
		var rec pictureRecord
		if err := json.Unmarshal(raw, &rec); err != nil || rec.ID == "" {
			logger.Warn("Business Central record without an id; no picture fetched", "entity", cfg.effectiveEntity())
			continue
		}
		if inFeed != nil {
			inFeed[rec.ID] = true
		}
		if err := c.syncPicture(ctx, connID, tenantID, cfg, tok, rec, &n); err != nil {
			return n, err
		}
	}
	return n, nil
}

// syncPicture brings the till in line with one record's picture: sends it
// when new or replaced, sends a .no-picture marker once when a picture the
// node remembers is gone, and does nothing for a record that has no picture
// now and had none before.
func (c *bcConsumer) syncPicture(ctx context.Context, connID, tenantID string, cfg *BCConfig,
	tok *oauthcc.Client, rec pictureRecord, n *pictureCounts) error {
	recordURL := fmt.Sprintf("%s/%s(%s)", cfg.companyURL(), cfg.effectiveEntity(), rec.ID)

	body, status, err := c.getStatus(ctx, tok, recordURL+"/picture")
	if err != nil {
		return fmt.Errorf("picture of %s %s: %w", cfg.effectiveEntity(), rec.ID, err)
	}
	var pic pictureInfo
	if status != http.StatusNotFound {
		if err := json.Unmarshal(body, &pic); err != nil {
			return fmt.Errorf("parse picture of %s %s: %w", cfg.effectiveEntity(), rec.ID, err)
		}
	}
	// 404, or an empty picture object: the record has no picture (now).
	if status == http.StatusNotFound || pic.ContentType == "" {
		return c.markRemovedIfSeen(ctx, connID, tenantID, cfg, rec, n)
	}
	// A replaced picture is a new media object with a new id; an unchanged
	// one is not downloaded again.
	if prev, ok := cfg.pictureSeen[rec.ID]; ok && pic.ID != "" && prev.PictureID == pic.ID {
		n.unchanged++
		return nil
	}

	sent, err := c.publishPicture(ctx, connID, tenantID, cfg, tok, rec, pic, recordURL)
	if err != nil {
		return fmt.Errorf("picture of %s %s: %w", cfg.effectiveEntity(), rec.ID, err)
	}
	if !sent {
		// Vanished between the metadata and the content request.
		return c.markRemovedIfSeen(ctx, connID, tenantID, cfg, rec, n)
	}
	cfg.pictureSeen[rec.ID] = seenPicture{PictureID: pic.ID, Number: rec.Number}
	n.sent++
	return nil
}

// markRemovedIfSeen sends the .no-picture marker for a record whose picture
// the node remembers, and forgets it so the marker goes out once. A record
// that never had a picture produces nothing.
func (c *bcConsumer) markRemovedIfSeen(ctx context.Context, connID, tenantID string, cfg *BCConfig,
	rec pictureRecord, n *pictureCounts) error {
	prev, ok := cfg.pictureSeen[rec.ID]
	if !ok {
		n.none++
		return nil
	}
	if rec.Number == "" {
		rec.Number = prev.Number
	}
	if err := c.publishNoPicture(ctx, connID, tenantID, cfg, rec); err != nil {
		return fmt.Errorf("no-picture marker for %s %s: %w", cfg.effectiveEntity(), rec.ID, err)
	}
	delete(cfg.pictureSeen, rec.ID)
	n.removed++
	return nil
}

// sweepPictures re-checks the pictures the node remembers for records this
// poll did not carry. An incremental feed only has records BC marked as
// modified, and a picture removed or replaced on an otherwise untouched
// record would never be noticed without this. One metadata request per
// remembered picture; the content is only fetched when it changed.
func (c *bcConsumer) sweepPictures(ctx context.Context, connID, tenantID string, cfg *BCConfig,
	tok *oauthcc.Client, inFeed map[string]bool, logger *slog.Logger) (pictureCounts, error) {
	var n pictureCounts
	ids := make([]string, 0, len(cfg.pictureSeen))
	for id := range cfg.pictureSeen {
		if !inFeed[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids) // deterministic order, and a stable place to resume after an error
	for _, id := range ids {
		rec := pictureRecord{ID: id, Number: cfg.pictureSeen[id].Number}
		n.swept++
		if err := c.syncPicture(ctx, connID, tenantID, cfg, tok, rec, &n); err != nil {
			return n, err
		}
	}
	if n.swept > 0 {
		logger.Debug("Business Central picture sweep", "checked", n.swept, "removed", n.removed, "replaced", n.sent)
	}
	return n, nil
}

// publishNoPicture sends the empty <number>.no-picture file. It travels like
// a picture (envelope.IsMedia) so the transforms pass it through and the
// file destinations keep its name next to the picture it replaces.
func (c *bcConsumer) publishNoPicture(ctx context.Context, connID, tenantID string, cfg *BCConfig, rec pictureRecord) error {
	env := envelope.New()
	env.TenantID = tenantID
	env.IntegrationID = connID
	env.ContentType = envelope.NoPictureContentType
	env.Source = "business-central-consumer"
	env.StepHistory = []string{"business-central-consumer"}
	env.Payload = []byte{}
	env.PayloadSize = 0
	meta := map[string]interface{}{
		"entity":    cfg.effectiveEntity(),
		"record_id": rec.ID,
		"marker":    "no-picture",
		"filename":  markerFilename(rec),
	}
	if rec.Number != "" {
		meta["number"] = rec.Number
	}
	env.Metadata = meta
	return c.publish(ctx, env)
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
	return fileBase(rec, pictureExt(contentType)) + "." + pictureExt(contentType)
}

// markerFilename names the removed-picture marker: the same base as the
// picture file, so the till matches them on the item number.
func markerFilename(rec pictureRecord) string {
	return fileBase(rec, "no-picture") + ".no-picture"
}

// fileBase is the record number when that makes a usable file name with
// the given extension, else the record id.
func fileBase(rec pictureRecord, ext string) string {
	if rec.Number != "" {
		base := sanitizeFilename(rec.Number)
		if agentproto.ValidFilename(base+"."+ext) == nil {
			return base
		}
	}
	return sanitizeFilename(rec.ID)
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
