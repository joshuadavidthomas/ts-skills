//go:build windows

package catalog

import (
	"fmt"
	"io/fs"

	"golang.org/x/sys/windows"
)

func setPrivateDirectoryPermissions(name string) error {
	user, err := currentUserSID()
	if err != nil {
		return fmt.Errorf("resolve current user SID: %w", err)
	}
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return fmt.Errorf("resolve LocalSystem SID: %w", err)
	}
	administrators, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return fmt.Errorf("resolve administrators SID: %w", err)
	}

	const inheritance = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	access, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{
		privateDirectoryAccess(user, windows.TRUSTEE_IS_USER, inheritance),
		privateDirectoryAccess(system, windows.TRUSTEE_IS_WELL_KNOWN_GROUP, inheritance),
		privateDirectoryAccess(administrators, windows.TRUSTEE_IS_WELL_KNOWN_GROUP, inheritance),
	}, nil)
	if err != nil {
		return fmt.Errorf("build private directory ACL: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		name,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		access,
		nil,
	); err != nil {
		return fmt.Errorf("set private directory ACL: %w", err)
	}
	return nil
}

func verifyPrivateDirectoryPermissions(name string, _ fs.FileInfo) error {
	descriptor, err := windows.GetNamedSecurityInfo(
		name,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		return fmt.Errorf("read private directory ACL: %w", err)
	}
	if descriptor == nil {
		return fmt.Errorf("private directory has no ACL")
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		return fmt.Errorf("read private directory DACL: %w", err)
	}
	if dacl == nil {
		return fmt.Errorf("private directory has no DACL")
	}
	return nil
}

func privateDirectoryAccess(sid *windows.SID, trusteeType windows.TRUSTEE_TYPE, inheritance uint32) windows.EXPLICIT_ACCESS {
	return windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  trusteeType,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}
}

func currentUserSID() (*windows.SID, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY, &token); err != nil {
		return nil, err
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}
