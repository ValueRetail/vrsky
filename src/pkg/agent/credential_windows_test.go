//go:build windows

package agent

import (
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// assertPrivate: the credential and its folder grant access to SYSTEM and
// Administrators only, and inherit nothing — ProgramData lets Users read by
// default, and that must not reach the credential.
func assertPrivate(t *testing.T, dir, file string) {
	t.Helper()
	for _, path := range []string{dir, file} {
		sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		sddl := sd.String()
		ctl, _, _ := sd.Control()
		if ctl&windows.SE_DACL_PROTECTED == 0 {
			t.Errorf("%s: DACL is not protected from inheritance: %s", path, sddl)
		}
		body := sddl[strings.Index(sddl, "D:"):]
		for _, ace := range strings.Split(body, "(")[1:] {
			trustee := strings.TrimSuffix(ace[strings.LastIndex(ace, ";")+1:], ")")
			if trustee != "SY" && trustee != "BA" {
				t.Errorf("%s grants access to %s: %s", path, trustee, sddl)
			}
		}
	}
}
