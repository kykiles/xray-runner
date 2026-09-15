//go:build windows

package app

import (
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// elevated reports whether this process runs with an elevated token; a var so
// the tests of an ordinary run behave the same from an elevated shell.
var elevated = func() bool { return windows.GetCurrentProcessToken().IsElevated() }

// createRuntimeDir makes this instance's runtime dir (11b). An ordinary run
// takes the user's temp dir: nobody who can change it has less power than the
// process. An elevated run cannot — an unelevated process of the same user has
// the same SID and full control of the profile. Its dir goes to the Windows
// temp dir, which only SYSTEM and Administrators may rename, under a DACL for
// those two alone: in the unelevated token Administrators is deny-only.
func createRuntimeDir() (string, error) {
	if !elevated() {
		return os.MkdirTemp("", "xray-runner-*")
	}
	win, err := windows.GetSystemWindowsDirectory()
	if err != nil {
		return "", fmt.Errorf("каталог для рабочего конфига: %w", err)
	}
	base := filepath.Join(win, "Temp")
	if err := checkTrustedDirs(base); err != nil {
		return "", err
	}
	return createProtectedDir(base)
}

// runtimeDirSDDL: owned by Administrators; full access for SYSTEM and
// Administrators, inherited by the config inside; nothing inherited from the
// parent (P).
const runtimeDirSDDL = "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// createProtectedDir creates a new directory in base with runtimeDirSDDL from
// the start — never for a moment under the parent's ACL — and reads back what
// the system actually set.
func createProtectedDir(base string) (string, error) {
	sd, err := windows.SecurityDescriptorFromString(runtimeDirSDDL)
	if err != nil {
		return "", err
	}
	sa := windows.SecurityAttributes{SecurityDescriptor: sd}
	sa.Length = uint32(unsafe.Sizeof(sa))
	dir := filepath.Join(base, "xray-runner-"+rand.Text())
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return "", err
	}
	// Fails on an existing name, so what it made is new and ours.
	if err := windows.CreateDirectory(p, &sa); err != nil {
		return "", fmt.Errorf("создание каталога для рабочего конфига %s: %w", dir, err)
	}
	got, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err == nil {
		err = checkProtectedSD(got)
	}
	if err != nil {
		_ = os.Remove(dir)
		return "", fmt.Errorf("права каталога для рабочего конфига %s: %w", dir, err)
	}
	return dir, nil
}

// checkProtectedSD confirms a descriptor is runtimeDirSDDL: owned by
// Administrators, not inheriting, access for SYSTEM and Administrators only.
func checkProtectedSD(sd *windows.SECURITY_DESCRIPTOR) error {
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !owner.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
		return fmt.Errorf("владелец %s, а не Администраторы", owner)
	}
	control, _, err := sd.Control()
	if err != nil {
		return err
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		return errors.New("права наследуются от родительского каталога")
	}
	aces, err := daclEntries(sd)
	if err != nil {
		return err
	}
	for _, e := range aces {
		if e.typ != windows.ACCESS_ALLOWED_ACE_TYPE ||
			!(e.sid.IsWellKnown(windows.WinLocalSystemSid) || e.sid.IsWellKnown(windows.WinBuiltinAdministratorsSid)) {
			return errors.New("в DACL есть записи не для SYSTEM и Администраторов")
		}
	}
	return nil
}

// checkTrustedDirs refuses a base that an unelevated process could rename,
// replace or re-permission: it and every directory above it must be a plain
// directory, not a junction or symlink, owned by SYSTEM, Administrators or
// TrustedInstaller, and grant nobody else the right to delete it, delete its
// entries, or change its owner or DACL. Others may add entries of their own,
// as in the Windows temp dir. None of it can then change after the check.
func checkTrustedDirs(base string) error {
	for dir := base; ; dir = filepath.Dir(dir) {
		if err := checkTrustedDir(dir); err != nil {
			return fmt.Errorf("каталог для рабочего конфига %s ненадёжен: %s — %w", base, dir, err)
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}

func checkTrustedDir(dir string) error {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return err
	}
	attrs, err := windows.GetFileAttributes(p)
	if err != nil {
		return err
	}
	if attrs&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || attrs&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return errors.New("не обычный каталог")
	}
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	return checkTrustedSD(sd)
}

// fileDeleteChild is FILE_DELETE_CHILD, which x/sys does not name.
const fileDeleteChild = 0x40

// untrustedRights let their holder move a directory away, or give themselves
// the power to.
const untrustedRights = windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | fileDeleteChild | windows.GENERIC_ALL

const trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

func trustedSID(sid *windows.SID) bool {
	return sid.IsWellKnown(windows.WinLocalSystemSid) ||
		sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) ||
		sid.String() == trustedInstallerSID
}

// checkTrustedSD is checkTrustedDirs' rule for one directory's descriptor.
// Inherit-only entries do not apply to the directory itself and deny entries
// only take rights away; an entry of any other kind is not guessed at.
func checkTrustedSD(sd *windows.SECURITY_DESCRIPTOR) error {
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !trustedSID(owner) {
		return fmt.Errorf("владелец %s", owner)
	}
	aces, err := daclEntries(sd)
	if err != nil {
		return err
	}
	for _, e := range aces {
		switch {
		case e.flags&windows.INHERIT_ONLY_ACE != 0, e.typ == windows.ACCESS_DENIED_ACE_TYPE:
		case e.typ != windows.ACCESS_ALLOWED_ACE_TYPE:
			return fmt.Errorf("запись DACL незнакомого типа %d", e.typ)
		case !trustedSID(e.sid) && e.mask&untrustedRights != 0:
			return fmt.Errorf("%s может удалить, переименовать его или сменить права (маска %#x)", e.sid, e.mask)
		}
	}
	return nil
}

type aceEntry struct {
	typ, flags uint8
	mask       windows.ACCESS_MASK
	sid        *windows.SID // only for allowed and denied entries, whose layout it is
}

// daclEntries lists a descriptor's DACL. No DACL at all grants everyone
// everything, and is an error.
func daclEntries(sd *windows.SECURITY_DESCRIPTOR) ([]aceEntry, error) {
	dacl, _, err := sd.DACL()
	if errors.Is(err, windows.ERROR_OBJECT_NOT_FOUND) || err == nil && dacl == nil {
		return nil, errors.New("нет DACL — доступ открыт всем")
	}
	if err != nil {
		return nil, err
	}
	entries := make([]aceEntry, 0, dacl.AceCount)
	for i := range uint32(dacl.AceCount) {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return nil, err
		}
		e := aceEntry{typ: ace.Header.AceType, flags: ace.Header.AceFlags, mask: ace.Mask}
		if e.typ == windows.ACCESS_ALLOWED_ACE_TYPE || e.typ == windows.ACCESS_DENIED_ACE_TYPE {
			e.sid = (*windows.SID)(unsafe.Pointer(&ace.SidStart)) //nolint:gosec // G103: an allowed/denied ACE keeps its SID in place from SidStart on
		}
		entries = append(entries, e)
	}
	return entries, nil
}
