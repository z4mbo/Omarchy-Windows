//go:build windows

package main

import (
	"errors"
	"fmt"
	"os/user"
	"strings"
	"syscall"
	"unsafe"
)

var (
	seamlessSidFromString = advapi32.NewProc("ConvertStringSidToSidW")
	seamlessSidLength     = advapi32.NewProc("GetLengthSid")
	seamlessInitACL       = advapi32.NewProc("InitializeAcl")
	seamlessAddACE        = advapi32.NewProc("AddAccessAllowedAce")
	seamlessSetNamedDACL  = advapi32.NewProc("SetNamedSecurityInfoW")
	seamlessLocalFree     = kernel32.NewProc("LocalFree")
)

// The fw_cfg token is readable by QEMU, which runs as this Windows user, but
// not by other ordinary host users or groups that may inherit read access to
// LocalAppData. Failure disables this optional bridge rather than exposing
// an unprotected bearer token.
func restrictSeamlessTokenWindows(path string) error {
	current, err := user.Current()
	if err != nil || !strings.HasPrefix(current.Uid, "S-1-") {
		return errors.New("current Windows SID unavailable")
	}
	sidText, err := syscall.UTF16PtrFromString(current.Uid)
	if err != nil {
		return err
	}
	var sid uintptr
	if ok, _, callErr := seamlessSidFromString.Call(uintptr(unsafe.Pointer(sidText)), uintptr(unsafe.Pointer(&sid))); ok == 0 {
		return fmt.Errorf("convert user SID: %w", callErr)
	}
	defer seamlessLocalFree.Call(sid)
	length, _, _ := seamlessSidLength.Call(sid)
	if length == 0 || length > 256 {
		return errors.New("invalid user SID length")
	}
	// ACL header (8 bytes), ACCESS_ALLOWED_ACE header+mask (8 bytes), SID.
	acl := make([]byte, 8+8+int(length)+4)
	if ok, _, callErr := seamlessInitACL.Call(uintptr(unsafe.Pointer(&acl[0])), uintptr(len(acl)), 2); ok == 0 {
		return fmt.Errorf("initialize token ACL: %w", callErr)
	}
	const fileAllAccess = 0x001F01FF
	if ok, _, callErr := seamlessAddACE.Call(uintptr(unsafe.Pointer(&acl[0])), 2, fileAllAccess, sid); ok == 0 {
		return fmt.Errorf("grant token file access: %w", callErr)
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	const (
		seFileObject              = 1
		daclSecurityInformation   = 0x00000004
		protectedDaclSecurityInfo = 0x80000000
	)
	status, _, _ := seamlessSetNamedDACL.Call(
		uintptr(unsafe.Pointer(name)), seFileObject,
		daclSecurityInformation|protectedDaclSecurityInfo,
		0, 0, uintptr(unsafe.Pointer(&acl[0])), 0,
	)
	if status != 0 {
		return syscall.Errno(status)
	}
	return nil
}
