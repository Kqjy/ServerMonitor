package main

import (
	"crypto/sha1"
	"encoding/binary"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const windowsServiceName = "sm-agent"

func agentServiceSID() string {
	encoded := utf16.Encode([]rune(strings.ToUpper(windowsServiceName)))
	raw := make([]byte, 0, len(encoded)*2)
	for _, unit := range encoded {
		raw = append(raw, byte(unit), byte(unit>>8))
	}
	sum := sha1.Sum(raw)
	parts := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		parts = append(parts, strconv.FormatUint(uint64(binary.LittleEndian.Uint32(sum[i*4:])), 10))
	}
	return "S-1-5-80-" + strings.Join(parts, "-")
}

func lockACLSystemAdminsOnly(path string) error {
	sidSys, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	sidAdm, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}

	inherit := uint32(windows.NO_INHERITANCE)
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		inherit = windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT
	}

	entries := []windows.EXPLICIT_ACCESS{
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(sidSys),
			},
		},
		{
			AccessPermissions: windows.GENERIC_ALL,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_GROUP,
				TrusteeValue: windows.TrusteeValueFromSID(sidAdm),
			},
		},
	}

	if sidSvc, svcErr := windows.StringToSid(agentServiceSID()); svcErr == nil {
		entries = append(entries, windows.EXPLICIT_ACCESS{
			AccessPermissions: windows.GENERIC_READ | windows.GENERIC_EXECUTE,
			AccessMode:        windows.GRANT_ACCESS,
			Inheritance:       inherit,
			Trustee: windows.TRUSTEE{
				TrusteeForm:  windows.TRUSTEE_IS_SID,
				TrusteeType:  windows.TRUSTEE_IS_USER,
				TrusteeValue: windows.TrusteeValueFromSID(sidSvc),
			},
		})
	}

	newACL, err := windows.ACLFromEntries(entries, nil)
	if err != nil {
		return err
	}

	secInfo := windows.SECURITY_INFORMATION(
		windows.DACL_SECURITY_INFORMATION |
			windows.OWNER_SECURITY_INFORMATION |
			windows.PROTECTED_DACL_SECURITY_INFORMATION,
	)

	return windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		secInfo,
		sidAdm,
		nil,
		newACL,
		nil,
	)
}
