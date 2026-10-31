//go:build windows

package auditlog

import (
	"os"
	"runtime"

	"golang.org/x/sys/windows"
)

func secureDirectory(path string) error           { return restrictToCurrentUser(path, true) }
func secureFile(path string, file *os.File) error { return restrictToCurrentUser(path, false) }

func restrictToCurrentUser(path string, isDirectory bool) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sid := user.User.Sid
	var pinner runtime.Pinner
	pinner.Pin(sid)
	defer pinner.Unpin()
	inheritance := uint32(windows.NO_INHERITANCE)
	if isDirectory {
		inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{AccessPermissions: windows.GENERIC_ALL, AccessMode: windows.SET_ACCESS, Inheritance: inheritance, Trustee: windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeType: windows.TRUSTEE_IS_USER, TrusteeValue: windows.TrusteeValueFromSID(sid)}}}, nil)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
