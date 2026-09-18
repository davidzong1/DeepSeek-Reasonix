//go:build windows

package winsandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const legacyCredentialDenyMask = windows.ACCESS_MASK(windows.FILE_GENERIC_READ | windows.FILE_GENERIC_EXECUTE)

// RepairLegacyCredentialDeny removes the exact current-user DENY RX ACE used
// by older Reasonix forbid-read handling, provided a dead sandbox run recorded
// this path. An ACE's mask alone cannot establish its origin. Ambiguous ACLs
// and live or unverifiable marker owners are never modified.
func RepairLegacyCredentialDeny(path string) error {
	if path == "" {
		return nil
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	canonicalPath := canonicalWindowsPath(path)

	lock, err := lockWindowsRoots([]string{path}, nil, "legacy credential ACL repair", 2*time.Second)
	if err != nil {
		return fmt.Errorf("lock legacy credential ACL repair: %w", err)
	}
	defer lock.release()

	userSID, err := currentProcessUserSIDString()
	if err != nil {
		return err
	}
	legacy, other, err := currentUserDenyACECounts(path, userSID)
	if err != nil {
		return err
	}
	if legacy == 0 {
		return nil
	}
	if other != 0 || legacy != 1 {
		return fmt.Errorf("refusing to alter non-legacy current-user deny ACL on %q", path)
	}
	markers := staleCredentialDenyMarkers(path, canonicalPath)
	if len(markers) == 0 {
		return fmt.Errorf("refusing to alter credential deny ACL without a stale sandbox record on %q", path)
	}

	removeErr := icacls(path, "/remove:d", "*"+userSID, "/C")
	remainingLegacy, remainingOther, verifyErr := currentUserDenyACECounts(path, userSID)
	if verifyErr == nil && remainingLegacy == 0 && remainingOther == 0 {
		for _, marker := range markers {
			sweepResidueMarkerFile(marker, sandboxResidueSIDs())
		}
		return nil
	}
	if removeErr != nil {
		return fmt.Errorf("remove legacy credential deny ACL: %w", removeErr)
	}
	if verifyErr != nil {
		return fmt.Errorf("verify legacy credential deny ACL removal: %w", verifyErr)
	}
	return fmt.Errorf("legacy credential deny ACL remains on %q", path)
}

func staleCredentialDenyMarkers(path, canonicalPath string) []string {
	entries, err := os.ReadDir(windowsDenyMarkerDir())
	if err != nil {
		return nil
	}
	var found []string
	for _, entry := range entries {
		pid, ok := windowsDenyMarkerOwnerPID(entry.Name())
		if entry.IsDir() || !ok {
			continue
		}
		marker := filepath.Join(windowsDenyMarkerDir(), entry.Name())
		for _, residue := range readResidueMarker(marker) {
			if residue.kind != residueDeny {
				continue
			}
			if !strings.EqualFold(filepath.Clean(residue.path), filepath.Clean(path)) &&
				!strings.EqualFold(canonicalWindowsPath(residue.path), canonicalPath) {
				continue
			}
			if !credentialMarkerOwnerExited(marker, pid) {
				return nil
			}
			found = append(found, marker)
			break
		}
	}
	return found
}

// canonicalWindowsPath expands 8.3 aliases without opening the protected file.
// Keep the original spelling on failure so the exact-path comparison remains
// available when a volume does not support long-name expansion.
func canonicalWindowsPath(path string) string {
	input, err := windows.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		return filepath.Clean(path)
	}
	buffer := make([]uint16, 32768)
	n, err := windows.GetLongPathName(input, &buffer[0], uint32(len(buffer)))
	if err != nil || n == 0 || n >= uint32(len(buffer)) {
		return filepath.Clean(path)
	}
	return filepath.Clean(windows.UTF16ToString(buffer[:n]))
}

func credentialMarkerOwnerExited(marker, pidText string) bool {
	if pidText == strconv.Itoa(os.Getpid()) {
		_, live := liveResidueMarkers.Load(marker)
		return !live
	}
	pid, err := strconv.ParseUint(pidText, 10, 32)
	if err != nil || pid == 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		// Access denied does not prove the owner is dead.
		return errors.Is(err, windows.ERROR_INVALID_PARAMETER)
	}
	defer windows.CloseHandle(handle)
	event, err := windows.WaitForSingleObject(handle, 0)
	return err == nil && event == windows.WAIT_OBJECT_0
}

func currentUserDenyACECounts(path, userSID string) (legacy, other int, err error) {
	sid, err := windows.StringToSid(userSID)
	if err != nil {
		return 0, 0, fmt.Errorf("parse current user SID: %w", err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return 0, 0, fmt.Errorf("read credential DACL %q: %w", path, err)
	}
	if sd == nil {
		return 0, 0, nil
	}
	acl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("read credential DACL entries %q: %w", path, err)
	}
	if acl == nil {
		return 0, 0, nil
	}
	for index := range uint32(acl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, index, &ace); err != nil {
			return 0, 0, fmt.Errorf("read credential DACL ACE %d for %q: %w", index, path, err)
		}
		if ace == nil || ace.Header.AceType == windows.ACCESS_ALLOWED_ACE_TYPE {
			continue
		}
		// SID-wide icacls removal must not erase object or conditional deny
		// entries whose trustee layout differs from a basic deny ACE.
		if ace.Header.AceType != windows.ACCESS_DENIED_ACE_TYPE {
			return 0, 0, fmt.Errorf("refusing to alter unsupported credential ACE type %d on %q", ace.Header.AceType, path)
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !windows.EqualSid(aceSID, sid) {
			continue
		}
		if ace.Header.AceFlags == 0 && ace.Mask == legacyCredentialDenyMask {
			legacy++
		} else {
			other++
		}
	}
	runtime.KeepAlive(sd)
	return legacy, other, nil
}
