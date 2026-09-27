//go:build windows

package main

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"unsafe"
)

func TestQMPControlPrivateDirectoryACL(t *testing.T) {
	dir, err := os.MkdirTemp(os.TempDir(), "tom-acl-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	// Elevated Windows runners may create temporary objects owned by the
	// Administrators group. Give this positive fixture the account owner that
	// the production verifier requires; do not relax the verifier itself.
	setQMPTestCurrentUserOwner(t, dir, 0x02000000) // FILE_FLAG_BACKUP_SEMANTICS
	previous := qmpControlDirectory
	qmpControlDirectory = func() (string, error) { return dir, nil }
	defer func() { qmpControlDirectory = previous }()

	if err := ensureQMPControlDirectoryACL(filepath.Dir(dir)); err == nil {
		t.Fatal("hardened a directory outside the configured QMP location")
	}
	unrelated := filepath.Join(dir, "unrelated.txt")
	if err := os.WriteFile(unrelated, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureQMPControlDirectoryACL(dir); err == nil {
		t.Fatal("hardened a nonempty directory and its unrelated contents")
	}
	if err := os.Remove(unrelated); err != nil {
		t.Fatal(err)
	}
	if err := ensureQMPControlDirectoryACL(dir); err != nil {
		t.Fatal(err)
	}
	if err := verifyQMPControlDirectoryACL(dir); err != nil {
		t.Fatalf("private directory was not verified: %v", err)
	}
	path, err := qmpControlPath(qmpNativePort)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	setQMPTestCurrentUserOwner(t, path, qmpOpenReparsePoint)
	if err := verifyQMPControlSocketACL(path); err != nil {
		t.Fatalf("private AF_UNIX socket was not verified: %v", err)
	}
	listener.Close()

	// A new child must inherit the private user ACE. The socket verifier must
	// still reject this ordinary file even when its ACL is otherwise private.
	child := filepath.Join(dir, "tools.sock")
	if err := os.WriteFile(child, []byte("not a socket"), 0600); err != nil {
		t.Fatal(err)
	}
	name, err := syscall.UTF16PtrFromString(child)
	if err != nil {
		t.Fatal(err)
	}
	var dacl, descriptor uintptr
	status, _, _ := qmpGetNamedSecurity.Call(uintptr(unsafe.Pointer(name)), qmpSEFileObject,
		qmpDACLInformation, 0, 0, uintptr(unsafe.Pointer(&dacl)), 0, uintptr(unsafe.Pointer(&descriptor)))
	if status != 0 {
		t.Fatal(syscall.Errno(status))
	}
	if err := verifyQMPUserOnlyDACL(dacl, descriptor, false); err != nil {
		qmpLocalFree.Call(descriptor)
		t.Fatalf("child did not inherit private ACL: %v", err)
	}
	qmpLocalFree.Call(descriptor)
	if err := verifyQMPControlSocketACL(child); err == nil {
		t.Fatal("accepted an ordinary file as the private QMP socket")
	}
	if err := verifyQMPControlSocketACL(filepath.Join(dir, "unknown.sock")); err == nil {
		t.Fatal("accepted an unconfigured endpoint")
	}
	if err := os.Remove(child); err != nil {
		t.Fatal(err)
	}

	// The fw_cfg token ACL uses a non-inheritable user ACE. It protects a
	// file, but is insufficient for a directory that will create sockets.
	if err := restrictSeamlessTokenWindows(dir); err != nil {
		t.Fatal(err)
	}
	if err := verifyQMPControlDirectoryACL(dir); err == nil {
		t.Fatal("accepted a private directory ACL without child inheritance")
	}
}

func TestQMPControlSocketRejectsNullDACL(t *testing.T) {
	dir, err := os.MkdirTemp(os.TempDir(), "tom-acl-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	setQMPTestCurrentUserOwner(t, dir, 0x02000000) // FILE_FLAG_BACKUP_SEMANTICS
	previous := qmpControlDirectory
	qmpControlDirectory = func() (string, error) { return dir, nil }
	defer func() { qmpControlDirectory = previous }()
	if err := ensureQMPControlDirectoryACL(dir); err != nil {
		t.Fatal(err)
	}
	path, err := qmpControlPath(qmpNativePort)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	setQMPTestCurrentUserOwner(t, path, qmpOpenReparsePoint)
	if err := verifyQMPControlSocketACL(path); err != nil {
		t.Fatalf("test socket was not private before tampering: %v", err)
	}
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	const writeDAC = 0x00040000
	handle, err := syscall.CreateFile(name, writeDAC,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, qmpOpenReparsePoint, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.CloseHandle(handle)
	setSecurity := advapi32.NewProc("SetSecurityInfo")
	status, _, _ := setSecurity.Call(uintptr(handle), qmpSEFileObject,
		qmpDACLInformation|qmpProtectedDACLInfo, 0, 0, 0, 0)
	if status != 0 {
		t.Fatal(syscall.Errno(status))
	}
	if err := verifyQMPControlSocketACL(path); err == nil {
		t.Fatal("accepted a QMP socket with a null DACL")
	}
}

func setQMPTestCurrentUserOwner(t *testing.T, path string, flags uint32) {
	t.Helper()
	sid, freeSID, err := qmpUserSID()
	if err != nil {
		t.Fatal(err)
	}
	defer freeSID()
	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	const writeOwner = 0x00080000
	handle, err := syscall.CreateFile(name, writeOwner,
		syscall.FILE_SHARE_READ|syscall.FILE_SHARE_WRITE|syscall.FILE_SHARE_DELETE,
		nil, syscall.OPEN_EXISTING, flags, 0)
	if err != nil {
		t.Fatalf("open positive ACL fixture to set owner: %v", err)
	}
	defer syscall.CloseHandle(handle)
	setSecurity := advapi32.NewProc("SetSecurityInfo")
	status, _, _ := setSecurity.Call(uintptr(handle), qmpSEFileObject,
		qmpOwnerInformation, sid, 0, 0, 0)
	if status != 0 {
		t.Fatalf("set positive ACL fixture owner: %v", syscall.Errno(status))
	}
}

func TestQMPControlACLRejectsOtherOwner(t *testing.T) {
	text, err := syscall.UTF16PtrFromString("S-1-1-0") // Everyone
	if err != nil {
		t.Fatal(err)
	}
	var other uintptr
	if ok, _, callErr := qmpSIDFromString.Call(uintptr(unsafe.Pointer(text)), uintptr(unsafe.Pointer(&other))); ok == 0 {
		t.Fatal(callErr)
	}
	defer qmpLocalFree.Call(other)
	if err := verifyQMPUserOwner(other); err == nil {
		t.Fatal("accepted a QMP object owned by a different SID")
	}
}

func TestQMPControlACLRejectsNullOrUnexpectedEntries(t *testing.T) {
	if err := verifyQMPUserOnlyDACL(0, 1, false); err == nil {
		t.Fatal("accepted null DACL")
	}
	if err := verifyQMPUserOnlyDACL(1, 0, false); err == nil {
		t.Fatal("accepted missing security descriptor")
	}
	acl := make([]byte, 32)
	acl[2] = byte(len(acl))
	acl[4] = 2 // Two ACEs are never accepted, even when one is the user.
	if err := verifyQMPUserOnlyDACL(uintptr(unsafe.Pointer(&acl[0])), 1, false); err == nil {
		t.Fatal("accepted additional access entries")
	}
}
