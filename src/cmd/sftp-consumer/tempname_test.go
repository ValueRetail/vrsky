package main

import (
	"context"
	"testing"
)

// Tests for #590: the SFTP consumer leaves temp names (dotfiles, ~ prefixes,
// .tmp and .part suffixes, the file consumer's rule from #374) alone until
// they are renamed, unless file_pattern names the temp form itself. The
// listing is faked at the sftpConn boundary; polls use the pdRig from
// done_persist_test.go, so the stability check (#562) and the done set (#538)
// run as in production.

// tnRename is what the uploader does when it is done: the same bytes and
// mtime under the final name (fakeSFTP.Rename only drops the old name).
func tnRename(r *pdRig, from, to string, sec int) {
	r.fake.mu.Lock()
	body := string(r.fake.files[from])
	r.fake.mu.Unlock()
	pdDel(r.fake, from)
	pdPut(r.fake, to, body, sec)
}

// Which names are published, by name and pattern. Four polls are plenty: the
// first sees a file, the second publishes it when it may be published.
func TestTempName_WhichNamesArePublished(t *testing.T) {
	cases := []struct {
		name    string
		file    string
		pattern string
		want    bool
	}{
		// No pattern: the file consumer's rule.
		{"plain name", "x.csv", "", true},
		{"part suffix", "x.csv.part", "", false},
		{"tmp suffix", "x.csv.tmp", "", false},
		{"part suffix, upper case", "X.CSV.PART", "", false},
		{"tmp suffix, mixed case", "x.Tmp", "", false},
		{"dotfile", ".x.csv", "", false},
		{"tilde prefix", "~x.csv", "", false},
		{"tilde lock file", "~$x.xlsx", "", false},
		{"only the suffix", "x.part", "", false},
		{"part in the middle", "x.part.csv", "", true},
		{"tmp in the middle", "x.tmp.csv", "", true},
		{"tilde in the middle", "x~1.csv", "", true},
		{"name that is just part", "part", "", true},
		{"name that is just tmp", "tmp", "", true},
		// A pattern that does not name the temp form does not let it through.
		{"star pattern, temp name", "x.csv.part", "*", false},
		{"star pattern, dotfile", ".hidden", "*", false},
		{"csv pattern, final name", "x.csv", "*.csv", true},
		{"csv pattern, tmp name", "x.csv.tmp", "*.csv", false},
		{"pattern for temp-looking csv", "x.tmp.csv", "*.csv", true},
		{"question mark pattern", "~x.csv", "?x.csv", false},
		// A pattern that names it does, if it matches.
		{"part pattern, part file", "x.csv.part", "*.part", true},
		// file_pattern is case-sensitive, as before; the temp rule is not.
		{"part pattern, upper case file", "X.PART", "*.part", false},
		{"upper case part pattern, upper case file", "X.PART", "*.PART", true},
		{"part pattern, tmp file", "x.csv.tmp", "*.part", false},
		{"part pattern, final name", "x.csv", "*.part", false},
		{"tmp pattern, tmp file", "x.tmp", "*.tmp", true},
		{"dot pattern, dotfile", ".x.csv", ".*", true},
		{"dot pattern, plain name", "x.csv", ".*", false},
		{"tilde pattern, tilde file", "~x.csv", "~*", true},
		{"literal temp name", "x.csv.part", "x.csv.part", true},
		{"literal temp name, other file", "y.csv.part", "x.csv.part", false},
		{"csv.part pattern", "x.csv.part", "*.csv.part", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPDRig(t, newPDStore(), &fakeSFTP{})
			r.cfg.FilePattern = tc.pattern
			r.boot()
			pdPut(r.fake, tc.file, "data", 0)
			r.polls(4)
			want := 0
			if tc.want {
				want = 1
			}
			if got := r.log.counts()[tc.file]; got != want {
				t.Errorf("%q with file_pattern %q published %d times, want %d", tc.file, tc.pattern, got, want)
			}
		})
	}
}

// AC 2: uploaded as x.part, renamed to x.csv: published once, as x.csv.
func TestTempName_UploadThenRenameIsPublishedOnce(t *testing.T) {
	cases := []struct {
		name string
		// pollsAsPart is how many polls see the file under the temp name
		// before the rename; more than two means it sat unchanged long
		// enough to pass the stability check under that name.
		pollsAsPart int
		grows       bool
		temp        string
		pattern     string
	}{
		{"renamed at once", 1, false, "x.csv.part", ""},
		{"sat unchanged past the settle gap", 5, false, "x.csv.part", ""},
		{"grew, then sat", 4, true, "x.csv.part", ""},
		{"tmp suffix", 3, false, "x.csv.tmp", ""},
		{"dot prefix", 3, false, ".x.csv", ""},
		{"tilde prefix", 3, false, "~x.csv", ""},
		{"with a csv pattern", 3, false, "x.csv.part", "*.csv"},
		{"with a star pattern", 3, false, "x.csv.part", "*"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newPDRig(t, newPDStore(), &fakeSFTP{})
			r.cfg.FilePattern = tc.pattern
			r.boot()
			body := `a,b`
			pdPut(r.fake, tc.temp, body, 0)
			for i := 0; i < tc.pollsAsPart; i++ {
				if tc.grows {
					body += `,c`
					pdPut(r.fake, tc.temp, body, i)
				}
				r.poll()
				if n := r.log.counts()[tc.temp]; n != 0 {
					t.Fatalf("poll %d: temp name %q published %d times, want 0", i+1, tc.temp, n)
				}
			}
			tnRename(r, tc.temp, "x.csv", 99)
			r.polls(4)
			counts := r.log.counts()
			if len(counts) != 1 || counts["x.csv"] != 1 {
				t.Fatalf("published %v, want x.csv once and nothing else", counts)
			}
			if got := r.log.payloads("x.csv"); len(got) != 1 || got[0] != body {
				t.Errorf("x.csv payload %q, want %q", got, body)
			}
		})
	}
}

// The done set and the stability check are as before: a final name still
// waits for two unchanged looks, is published once, and is not published
// again, also after a restart; a temp name is never marked done.
func TestTempName_DoneSetAndStabilityUnchanged(t *testing.T) {
	r := newPDRig(t, newPDStore(), &fakeSFTP{})
	r.boot()
	pdPut(r.fake, "x.csv.part", "abc", 0)
	r.polls(3)
	if r.done.Len() != 0 {
		t.Fatalf("done set holds %d names after polls that saw only a temp name, want 0", r.done.Len())
	}
	tnRename(r, "x.csv.part", "x.csv", 5)
	r.poll()
	if n := r.log.counts()["x.csv"]; n != 0 {
		t.Fatalf("x.csv published %d times at its first look, want 0 (stability check)", n)
	}
	r.poll()
	if n := r.log.counts()["x.csv"]; n != 1 {
		t.Fatalf("x.csv published %d times at its second look, want 1", n)
	}
	r.polls(3)
	r.boot()
	r.polls(3)
	if counts := r.log.counts(); len(counts) != 1 || counts["x.csv"] != 1 {
		t.Errorf("published %v after more polls and a restart, want x.csv once", counts)
	}
}

// A temp name is left in the folder: after_action delete and move never
// touch it, and it is not counted as waiting, so the poller keeps to its
// interval instead of looking every settle gap.
func TestTempName_LeftAloneAndNotWaitedOn(t *testing.T) {
	for _, action := range []string{"none", "delete", "move"} {
		t.Run(action, func(t *testing.T) {
			r := newPDRig(t, newPDStore(), &fakeSFTP{})
			r.cfg.AfterAction = action
			r.cfg.MoveDir = "/done"
			r.boot()
			pdPut(r.fake, "x.csv.part", "abc", 0)
			pdPut(r.fake, ".lock", "", 0)
			r.poll()
			waiting, unreachable := r.c.pollOnce(context.Background(), r.conn, r.tenant, &r.cfg, r.done, r.pending, r.c.logger)
			if waiting != 0 || unreachable {
				t.Errorf("pollOnce returned waiting=%d unreachable=%v, want 0 and false", waiting, unreachable)
			}
			if len(r.pending) != 0 {
				t.Errorf("pending %v, want no sighting of a temp name", r.pending)
			}
			r.fake.mu.Lock()
			defer r.fake.mu.Unlock()
			if len(r.fake.removed) != 0 || len(r.fake.moved) != 0 {
				t.Errorf("removed %v, moved %v, want the temp names left in place", r.fake.removed, r.fake.moved)
			}
		})
	}
}

// A temp name next to a real file does not hold the real file back.
func TestTempName_RealFileNextToTempFile(t *testing.T) {
	r := newPDRig(t, newPDStore(), &fakeSFTP{})
	r.boot()
	pdPut(r.fake, "a.csv", "1", 0)
	pdPut(r.fake, "b.csv.part", "2", 0)
	r.polls(3)
	if counts := r.log.counts(); len(counts) != 1 || counts["a.csv"] != 1 {
		t.Errorf("published %v, want a.csv once and nothing else", counts)
	}
}

// The rule itself: the file consumer's names, and when a pattern asks for
// them. The first table must stay equal to isTempName in
// src/cmd/file-consumer (#374).
func TestTempName_Rule(t *testing.T) {
	for name, want := range map[string]bool{
		"":             false,
		"x.csv":        false,
		"x.part.csv":   false,
		"x~":           false,
		"part":         false,
		".":            true,
		".x":           true,
		"~":            true,
		"~$x.xlsx":     true,
		"x.part":       true,
		"x.PART":       true,
		"x.tmp":        true,
		"x.Tmp":        true,
		"x.csv.part":   true,
		"x.csv.tmp":    true,
		"dir.part/x":   false,
		"x.tmp.backup": false,
	} {
		if got := isTempName(name); got != want {
			t.Errorf("isTempName(%q) = %v, want %v", name, got, want)
		}
	}
	for pattern, want := range map[string]bool{
		"":           false,
		"*":          false,
		"*.csv":      false,
		"?*":         false,
		"[.]*":       false,
		"x.part.*":   false,
		"*.part":     true,
		"*.PART":     true,
		"*.tmp":      true,
		"*.csv.tmp":  true,
		".*":         true,
		".x*":        true,
		"~*":         true,
		"x.csv.part": true,
	} {
		if got := patternNamesTemp(pattern); got != want {
			t.Errorf("patternNamesTemp(%q) = %v, want %v", pattern, got, want)
		}
	}
}
