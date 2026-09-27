//go:build windows

package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"
)

var (
	qmpSIDFromString        = advapi32.NewProc("ConvertStringSidToSidW")
	qmpInitializeACL        = advapi32.NewProc("InitializeAcl")
	qmpAddAllowedACEEx      = advapi32.NewProc("AddAccessAllowedAceEx")
	qmpSetNamedSecurity     = advapi32.NewProc("SetNamedSecurityInfoW")
	qmpGetNamedSecurity     = advapi32.NewProc("GetNamedSecurityInfoW")
	qmpGetHandleSecurity    = advapi32.NewProc("GetSecurityInfo")
	qmpGetDescriptorControl = advapi32.NewProc("GetSecurityDescriptorControl")
	qmpEqualSID             = advapi32.NewProc("EqualSid")
	qmpLocalFree            = kernel32.NewProc("LocalFree")
)

const (
	qmpSEFileObject        = 1
	qmpOwnerInformation    = 0x00000001
	qmpDACLInformation     = 0x00000004
	qmpProtectedDACLInfo   = 0x80000000
	qmpDACLProtected       = 0x1000
	qmpObjectInheritACE    = 0x01
	qmpContainerInheritACE = 0x02
	qmpFileAllAccess       = 0x001F01FF
	qmpReadControl         = 0x00020000
	qmpOpenReparsePoint    = 0x00200000
)

func qmpUserSID() (uintptr, func(), error) {
	current, err := user.Current()
	if err != nil || !strings.HasPrefix(current.Uid, "S-1-") {
		return 0, nil, errors.New("current Windows SID unavailable")
	}
	text, err := syscall.UTF16PtrFromString(current.Uid)
	if err != nil {
		return 0, nil, err
	}
	var sid uintptr
	if ok, _, callErr := qmpSIDFromString.Call(uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(&sid))); ok == 0 {
		return 0, nil, fmt.Errorf("convert current user SID: %w", callErr)
	}
	return sid, func() { qmpLocalFree.Call(sid) }, nil
}

// ensureQMPControlDirectoryACL is deliberately opt-in. The existing QEMU
// launch path is unchanged until a patched runtime and its socket ACL have
// been physically verified. The foreground handoff must call this before
// spawning QEMU, then verifyQMPControlSocketACL after QEMU creates native.sock.
func ensureQMPControlDirectoryACL(dir string) error {
	expected, err := qmpControlDirectory()
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(dir), filepath.Clean(expected)) {
		return errors.New("QMP ACL target is not the app-owned control directory")
	}
	if err := validateMovePath(dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("QMP ACL target is not a plain directory")
	}
	if err := verifyQMPNamedOwner(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("QMP directory is not empty before private ACL setup")
	}
	sid, freeSID, err := qmpUserSID()
	if err != nil {
		return err
	}
	defer freeSID()
	// One full-access ACE for the current user, inheritable by files and
	// directories created beneath this protected app-owned directory.
	acl := make([]byte, 8+8+256+4)
	if ok, _, callErr := qmpInitializeACL.Call(uintptr(unsafe.Pointer(&acl[0])), uintptr(len(acl)), 2); ok == 0 {
		return fmt.Errorf("initialize QMP ACL: %w", callErr)
	}
	if ok, _, callErr := qmpAddAllowedACEEx.Call(uintptr(unsafe.Pointer(&acl[0])), 2, qmpObjectInheritACE|qmpContainerInheritACE, qmpFileAllAccess, sid); ok == 0 {
		return fmt.Errorf("grant current user QMP directory access: %w", callErr)
	}
	name, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	status, _, _ := qmpSetNamedSecurity.Call(uintptr(unsafe.Pointer(name)), qmpSEFileObject,
		qmpDACLInformation|qmpProtectedDACLInfo, 0, 0, uintptr(unsafe.Pointer(&acl[0])), 0)
	if status != 0 {
		return fmt.Errorf("set private QMP directory ACL: %w", syscall.Errno(status))
	}
	return verifyQMPControlDirectoryACL(dir)
}

func verifyQMPNamedOwner(path string) error {
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	var owner, descriptor uintptr
	status, _, _ := qmpGetNamedSecurity.Call(uintptr(unsafe.Pointer(name)), qmpSEFileObject,
		qmpOwnerInformation, uintptr(unsafe.Pointer(&owner)), 0, 0, 0,
		uintptr(unsafe.Pointer(&descriptor)))
	if status != 0 {
		return fmt.Errorf("read QMP object owner: %w", syscall.Errno(status))
	}
	defer qmpLocalFree.Call(descriptor)
	return verifyQMPUserOwner(owner)
}

func verifyQMPControlDirectoryACL(dir string) error {
	expected, err := qmpControlDirectory()
	if err != nil {
		return err
	}
	if !strings.EqualFold(filepath.Clean(dir), filepath.Clean(expected)) {
		return errors.New("QMP ACL target is not the app-owned control directory")
	}
	if err := validateMovePath(dir); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("QMP control directory is missing or redirected")
	}
	name, err := syscall.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	var owner, dacl, descriptor uintptr
	status, _, _ := qmpGetNamedSecurity.Call(uintptr(unsafe.Pointer(name)), qmpSEFileObject,
		qmpOwnerInformation|qmpDACLInformation, uintptr(unsafe.Pointer(&owner)), 0,
		uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&descriptor)))
	if status != 0 {
		return fmt.Errorf("read QMP directory ACL: %w", syscall.Errno(status))
	}
	defer qmpLocalFree.Call(descriptor)
	if err := verifyQMPUserOwner(owner); err != nil {
		return err
	}
	return verifyQMPUserOnlyDACL(dacl, descriptor, true)
}

// verifyQMPControlSocketACL opens the AF_UNIX reparse point itself, rather
// than trusting a path-based check that might inspect a reparse target. A
// failure disables only the proposed privileged foreground operation.
func verifyQMPControlSocketACL(path string) error {
	known := false
	for _, role := range []int{qmpToolsPort, qmpFwdPort, qmpSupPort, qmpNativePort} {
		candidate, err := qmpControlPath(role)
		if err != nil {
			return err
		}
		if strings.EqualFold(filepath.Clean(path), filepath.Clean(candidate)) {
			known = true
			break
		}
	}
	if !known {
		return errors.New("QMP socket is not an app-owned control endpoint")
	}
	if err := verifyQMPControlDirectoryACL(filepath.Dir(path)); err != nil {
		return err
	}
	if !isQMPControlSocket(path) {
		return errors.New("QMP endpoint is not a Windows AF_UNIX socket")
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	handle, err := syscall.CreateFile(name, qmpReadControl,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, qmpOpenReparsePoint, 0)
	if err != nil {
		return fmt.Errorf("open QMP socket security descriptor: %w", err)
	}
	defer syscall.CloseHandle(handle)
	var owner, dacl, descriptor uintptr
	status, _, _ := qmpGetHandleSecurity.Call(uintptr(handle), qmpSEFileObject,
		qmpOwnerInformation|qmpDACLInformation, uintptr(unsafe.Pointer(&owner)), 0,
		uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&descriptor)))
	if status != 0 {
		return fmt.Errorf("read QMP socket ACL: %w", syscall.Errno(status))
	}
	defer qmpLocalFree.Call(descriptor)
	if !isQMPControlSocket(path) {
		return errors.New("QMP endpoint changed during ACL verification")
	}
	if err := verifyQMPUserOwner(owner); err != nil {
		return err
	}
	return verifyQMPUserOnlyDACL(dacl, descriptor, false)
}

func verifyQMPUserOwner(owner uintptr) error {
	if owner == 0 {
		return errors.New("QMP object has no owner")
	}
	sid, freeSID, err := qmpUserSID()
	if err != nil {
		return err
	}
	defer freeSID()
	if equal, _, _ := qmpEqualSID.Call(owner, sid); equal == 0 {
		return errors.New("QMP object is owned by another SID")
	}
	return nil
}

func verifyQMPUserOnlyDACL(dacl, descriptor uintptr, directory bool) error {
	if dacl == 0 || descriptor == 0 {
		return errors.New("QMP object has a null DACL")
	}
	if directory {
		var control uint16
		var revision uint32
		if ok, _, callErr := qmpGetDescriptorControl.Call(descriptor, uintptr(unsafe.Pointer(&control)), uintptr(unsafe.Pointer(&revision))); ok == 0 {
			return fmt.Errorf("read QMP directory security control: %w", callErr)
		}
		if control&qmpDACLProtected == 0 {
			return errors.New("QMP directory DACL inherits permissions")
		}
	}
	aclSize := binary.LittleEndian.Uint16(unsafe.Slice((*byte)(unsafe.Pointer(dacl)), 8)[2:4])
	if aclSize < 24 || aclSize > 65532 {
		return errors.New("QMP object has invalid ACL size")
	}
	acl := unsafe.Slice((*byte)(unsafe.Pointer(dacl)), int(aclSize))
	if binary.LittleEndian.Uint16(acl[4:6]) != 1 {
		return errors.New("QMP object ACL is not current-user only")
	}
	ace := acl[8:]
	aceSize := int(binary.LittleEndian.Uint16(ace[2:4]))
	if aceSize < 16 || aceSize > len(ace) || ace[0] != 0 || binary.LittleEndian.Uint32(ace[4:8]) != qmpFileAllAccess {
		return errors.New("QMP object has unexpected access entry")
	}
	if directory && ace[1]&(qmpObjectInheritACE|qmpContainerInheritACE) != qmpObjectInheritACE|qmpContainerInheritACE {
		return errors.New("QMP directory access does not inherit to sockets")
	}
	sidBytes := ace[8:aceSize]
	if len(sidBytes) < 8 || sidBytes[0] != 1 || len(sidBytes) != 8+4*int(sidBytes[1]) {
		return errors.New("QMP object has invalid user SID")
	}
	sid, freeSID, err := qmpUserSID()
	if err != nil {
		return err
	}
	defer freeSID()
	if equal, _, _ := qmpEqualSID.Call(uintptr(unsafe.Pointer(&sidBytes[0])), sid); equal == 0 {
		return errors.New("QMP object grants access to another SID")
	}
	return nil
}
