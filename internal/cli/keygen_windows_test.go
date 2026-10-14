//go:build windows

package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"github.com/Boulea7/shellpier/internal/platform"
	"golang.org/x/sys/windows"
)

// Verify Windows security descriptors directly, independent of shell modules.
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
		t.Run(filepath.Base(path), func(t *testing.T) {
			sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.KeepAlive(sd)
			owner, _, err := sd.Owner()
			if err != nil {
				t.Fatal(err)
			}
			if owner == nil || !owner.Equals(user.User.Sid) {
				t.Fatal("owner is not the current user")
			}
			control, _, err := sd.Control()
			if err != nil {
				t.Fatal(err)
			}
			if control&windows.SE_DACL_PROTECTED == 0 {
				t.Fatal("DACL inheritance is not protected")
			}
			acl, _, err := sd.DACL()
			if err != nil {
				t.Fatal(err)
			}
			if acl == nil || acl.AceCount != 1 {
				t.Fatal("DACL must contain exactly one explicit permission")
			}
			var ace *windows.ACCESS_ALLOWED_ACE
			if err := windows.GetAce(acl, 0, &ace); err != nil {
				t.Fatal(err)
			}
			if ace == nil || ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
				t.Fatal("permission is not an explicit Allow ACE")
			}
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
			if !sid.Equals(user.User.Sid) {
				t.Fatal("permission grants access to another SID")
			}
			// Windows maps GENERIC_ALL to the file object's full-control mask.
			const fileAllAccess windows.ACCESS_MASK = 0x001f01ff
			if ace.Mask != windows.GENERIC_ALL && ace.Mask != fileAllAccess {
				t.Fatalf("permission is not full control: %#x", ace.Mask)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.IsDir() {
				if ace.Header.AceFlags&windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT != windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT {
					t.Fatal("private directory does not inherit permissions to its children")
				}
			}
		})
	}
}
