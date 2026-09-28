package envelope

import "testing"

func TestIsMedia(t *testing.T) {
	for ct, want := range map[string]bool{
		"image/jpeg":                     true,
		"image/png; charset=binary":      true,
		"IMAGE/GIF":                      true,
		"audio/mpeg":                     true,
		"video/mp4":                      true,
		"application/pdf":                true,
		"application/json":               false,
		"text/csv":                       false,
		"application/xml":                false,
		"application/octet-stream":       false, // uploads of unknown files, CSV included
		"":                               false,
		"application/x-ndjson":           false,
		"text/plain; charset=utf-8":      false,
		"multipart/form-data; boundary=": false,
	} {
		if got := IsMedia(ct); got != want {
			t.Errorf("IsMedia(%q) = %v, want %v", ct, got, want)
		}
	}
}
