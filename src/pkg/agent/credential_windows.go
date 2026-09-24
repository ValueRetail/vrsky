//go:build windows

package agent

import (
	"os"

	"golang.org/x/sys/windows"
)

// Access for SYSTEM (the service account) and the local Administrators group
// only, with inheritance from the parent cut ("P"), so nothing granted higher up
// the tree — ProgramData lets Users read by default — reaches the credential.
const (
	privateDirSDDL  = "D:PAI(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	privateFileSDDL = "D:PAI(A;;FA;;;SY)(A;;FA;;;BA)"
)

func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return applySDDL(dir, privateDirSDDL)
}

func restrictFile(path string) error { return applySDDL(path, privateFileSDDL) }

func applySDDL(path, sddl string) error {
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil)
}
