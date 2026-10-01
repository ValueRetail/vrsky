package envelope

import (
	"mime"
	"strings"
)

// IsMedia reports whether contentType names a file to be carried as-is — an
// image, audio, video or PDF — rather than records a transform can parse.
//
// Transforms (data-converter, data-filter) pass such messages through
// unchanged, and the file-writing destinations keep the file name they carry
// even when a filename pattern is set for the records beside them. That is
// what lets one pipeline carry a catalogue's records and its pictures (#281).
//
// application/octet-stream is deliberately NOT media: file and agent uploads
// stamp it on anything they cannot name, CSV included, and the converter
// sniffs and converts those today.
//
// NoPictureContentType counts as media too: it is an empty marker file
// (<number>.no-picture) that tells a till a record's picture was removed,
// and it must travel exactly like the picture it replaces.
func IsMedia(contentType string) bool {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mt = strings.ToLower(strings.TrimSpace(contentType))
	}
	return strings.HasPrefix(mt, "image/") || strings.HasPrefix(mt, "audio/") ||
		strings.HasPrefix(mt, "video/") || mt == "application/pdf" || mt == NoPictureContentType
}

// NoPictureContentType marks an empty file named <number>.no-picture: the
// record still exists but its picture was removed (Business Central pictures,
// plans/bc-picture-deletions.md).
const NoPictureContentType = "application/vnd.vrsky.no-picture"
