//go:build windows

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unsafe"

	"github.com/Boulea7/shellpier/internal/platform"
	"golang.org/x/sys/windows"
)

// Verify permissions, not ACE layout: Windows may split generic inheritable
// permissions into effective and inherit-only ACEs during normalization.
// https://learn.microsoft.com/en-us/windows/win32/secauthz/ace-inheritance-rules
func TestKeygenWindowsPrivateACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "keys")
	result, err := generateKeyPair(dir, platform.SecureFile)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{dir, result.PrivateKeyPath, result.PublicKeyPath} {
		t.Run(filepath.Base(path), func(t *testing.T) { assertPrivateWindowsACL(t, path, user.User.Sid, true) })
	}
	// New descendants are deliberately not secured again. Their DACLs must be
	// private through the parent inheritance rules alone.
	childDir := filepath.Join(dir, "inheritance-probe")
	if err := os.Mkdir(childDir, 0700); err != nil {
		t.Fatal(err)
	}
	childFile := filepath.Join(childDir, "probe.txt")
	if err := os.WriteFile(childFile, []byte("nonsecret ACL probe"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Run("inherited-directory", func(t *testing.T) { assertPrivateWindowsACL(t, childDir, user.User.Sid, false) })
	t.Run("inherited-file", func(t *testing.T) { assertPrivateWindowsACL(t, childFile, user.User.Sid, false) })
}

func assertPrivateWindowsACL(t *testing.T, path string, currentUser *windows.SID, secured bool) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.KeepAlive(sd)
	if secured {
		owner, _, err := sd.Owner()
		if err != nil {
			t.Fatal(err)
		}
		if owner == nil || !owner.Equals(currentUser) {
			t.Fatal("owner is not the current user")
		}
		control, _, err := sd.Control()
		if err != nil {
			t.Fatal(err)
		}
		if control&windows.SE_DACL_PROTECTED == 0 {
			t.Fatal("DACL inheritance is not protected")
		}
	}
	acl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if acl == nil || acl.AceCount == 0 {
		t.Fatal("DACL is missing or empty")
	}
	const fileAllAccess windows.ACCESS_MASK = 0x001f01ff
	var effective, objectInheritance, containerInheritance windows.ACCESS_MASK
	var diagnostics []string
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		if ace == nil {
			t.Fatalf("nil ACE at index %d", i)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("ACE %d type=%d flags=%#x mask=%#x is not an Allow ACE", i, ace.Header.AceType, ace.Header.AceFlags, ace.Mask)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		diagnostic := fmt.Sprintf("ACE %d type=%d flags=%#x mask=%#x SID=%s", i, ace.Header.AceType, ace.Header.AceFlags, ace.Mask, sid.String())
		diagnostics = append(diagnostics, diagnostic)
		if !sid.Equals(currentUser) {
			t.Fatalf("permission grants access to another SID: %s", diagnostic)
		}
		if secured && ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			t.Fatalf("secured object has an inherited permission: %s", diagnostic)
		}
		mask := ace.Mask
		if mask&windows.GENERIC_ALL != 0 {
			mask |= fileAllAccess
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 {
			effective |= mask
		}
		if ace.Header.AceFlags&windows.NO_PROPAGATE_INHERIT_ACE == 0 {
			if ace.Header.AceFlags&windows.OBJECT_INHERIT_ACE != 0 {
				objectInheritance |= mask
			}
			if ace.Header.AceFlags&windows.CONTAINER_INHERIT_ACE != 0 {
				containerInheritance |= mask
			}
		}
	}
	if effective&fileAllAccess != fileAllAccess {
		t.Fatalf("current object lacks full control: %s", strings.Join(diagnostics, "; "))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.IsDir() && (objectInheritance&fileAllAccess != fileAllAccess || containerInheritance&fileAllAccess != fileAllAccess) {
		t.Fatalf("directory does not propagate full control to files and directories: %s", strings.Join(diagnostics, "; "))
	}
}
