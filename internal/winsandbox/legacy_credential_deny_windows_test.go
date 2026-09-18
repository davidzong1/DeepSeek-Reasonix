//go:build windows

package winsandbox

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestRepairLegacyCredentialDenyRemovesExactCurrentUserACE(t *testing.T) {
	t.Setenv("TEMP", t.TempDir())
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("KEY=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	userSID, err := currentProcessUserSIDString()
	if err != nil {
		t.Fatal(err)
	}
	if err := denyAppContainerSIDsWithInheritance(path, []string{userSID}, "RX", false); err != nil {
		t.Fatal(err)
	}
	legacy, other, err := currentUserDenyACECounts(path, userSID)
	if err != nil || legacy != 1 || other != 0 {
		t.Fatalf("deny counts before repair = legacy:%d other:%d err:%v", legacy, other, err)
	}
	marker := writeStaleCredentialMarker(t, path)

	if err := RepairLegacyCredentialDeny(path); err != nil {
		t.Fatal(err)
	}
	legacy, other, err = currentUserDenyACECounts(path, userSID)
	if err != nil || legacy != 0 || other != 0 {
		t.Fatalf("deny counts after repair = legacy:%d other:%d err:%v", legacy, other, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "KEY=value\n" {
		t.Fatalf("credential after repair = %q, %v", data, err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("stale marker survived repair: %v", err)
	}
	if err := denyAppContainerSIDsWithInheritance(path, []string{userSID}, "RX", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeDeniedAppContainerSIDs(path, []string{userSID}) })
	if err := RepairLegacyCredentialDeny(path); err == nil {
		t.Fatal("repair reused a consumed stale marker")
	}
}

func writeStaleCredentialMarker(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(windowsDenyMarkerDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// An unregistered self-PID marker represents a crashed predecessor after
	// PID reuse, matching the existing sandbox residue lifecycle.
	marker := filepath.Join(windowsDenyMarkerDir(), strconv.Itoa(os.Getpid())+"-credential-test.txt")
	if err := os.WriteFile(marker, []byte("deny\t"+path+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return marker
}

func TestRepairLegacyCredentialDenyMatchesFileAcrossPathAliases(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMP", tmp)
	t.Setenv("TEMP", tmp)
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("KEY=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pathUTF16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	shortBuffer := make([]uint16, 32768)
	n, err := windows.GetShortPathName(pathUTF16, &shortBuffer[0], uint32(len(shortBuffer)))
	if err != nil || n == 0 || n >= uint32(len(shortBuffer)) {
		t.Skipf("8.3 path aliases unavailable: %v", err)
	}
	alias := windows.UTF16ToString(shortBuffer[:n])
	if strings.EqualFold(filepath.Clean(alias), filepath.Clean(path)) {
		t.Skip("fixture path has no distinct 8.3 alias")
	}
	userSID, err := currentProcessUserSIDString()
	if err != nil {
		t.Fatal(err)
	}
	if err := denyAppContainerSIDsWithInheritance(path, []string{userSID}, "RX", false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeDeniedAppContainerSIDs(path, []string{userSID}) })
	writeStaleCredentialMarker(t, alias)
	if err := RepairLegacyCredentialDeny(path); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "KEY=value\n" {
		t.Fatalf("credential after aliased repair = %q, %v", data, err)
	}
}

func TestRepairLegacyCredentialDenyPreservesUnattributedAndLiveACL(t *testing.T) {
	for _, source := range []string{"missing marker", "live marker", "wrong path"} {
		t.Run(source, func(t *testing.T) {
			t.Setenv("TEMP", t.TempDir())
			path := filepath.Join(t.TempDir(), ".env")
			if err := os.WriteFile(path, []byte("KEY=value\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			userSID, err := currentProcessUserSIDString()
			if err != nil {
				t.Fatal(err)
			}
			if err := denyAppContainerSIDsWithInheritance(path, []string{userSID}, "RX", false); err != nil {
				t.Fatal(err)
			}
			switch source {
			case "live marker":
				marker := writeStaleCredentialMarker(t, path)
				liveResidueMarkers.Store(marker, struct{}{})
				t.Cleanup(func() { liveResidueMarkers.Delete(marker) })
			case "wrong path":
				writeStaleCredentialMarker(t, path+"-other")
			}
			before, err := windowsPathDACLSDDL(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := RepairLegacyCredentialDeny(path); err == nil {
				t.Fatal("repair accepted an unattributed or live deny")
			}
			after, err := windowsPathDACLSDDL(path)
			if err != nil || after != before {
				t.Fatalf("DACL changed: before %s, after %s, err %v", before, after, err)
			}
		})
	}
}

func TestRepairLegacyCredentialDenyLeavesOrdinaryACLAlone(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("KEY=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := windowsPathDACLSDDL(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := RepairLegacyCredentialDeny(path); err != nil {
		t.Fatal(err)
	}
	after, err := windowsPathDACLSDDL(path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("ordinary DACL changed:\nbefore %s\nafter  %s", before, after)
	}
}

func TestRepairLegacyCredentialDenyRefusesMixedCurrentUserDenyACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte("KEY=value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	userSID, err := currentProcessUserSIDString()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"D:(D;;0x%x;;;%s)(D;;0x2;;;%s)(A;;FA;;;%s)",
		uint32(legacyCredentialDenyMask), userSID, userSID, userSID,
	))
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	runtime.KeepAlive(sd)
	legacy, other, err := currentUserDenyACECounts(path, userSID)
	if err != nil || legacy != 1 || other == 0 {
		t.Fatalf("deny counts before repair = legacy:%d other:%d err:%v", legacy, other, err)
	}
	before, err := windowsPathDACLSDDL(path)
	if err != nil {
		t.Fatal(err)
	}

	if err := RepairLegacyCredentialDeny(path); err == nil {
		t.Fatal("RepairLegacyCredentialDeny accepted mixed current-user deny ACL")
	}
	after, err := windowsPathDACLSDDL(path)
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatalf("mixed DACL changed:\nbefore %s\nafter  %s", before, after)
	}
}

func windowsPathDACLSDDL(path string) (string, error) {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil || sd == nil {
		return "", err
	}
	return sd.String(), nil
}
