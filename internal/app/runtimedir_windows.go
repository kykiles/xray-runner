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

// checkRuntimeBase is checkTrustedDirs; a var so the tests of what happens
// after it do not turn into a check of whatever ACL the machine's temp dir
// happens to carry. What it does has tests of its own.
var checkRuntimeBase = checkTrustedDirs

// createRuntimeDir makes this instance's runtime dir (11b). Both runs create it
// with a DACL of its own — never for a moment under the one the temp dir hands
// down — and both refuse a base that someone they do not trust could rename out
// from under the running core. A random name and a Unix mode are neither of
// those things on Windows: what a file grants is its DACL, and an inheritable
// entry on the temp dir reaches everything made inside it.
//
// The two runs differ in whom they trust. An ordinary run trusts its own user:
// nobody holding that SID has less power than this process. An elevated run
// cannot — an unelevated process of the same user has the same SID and full
// control of the profile — so its dir goes to the Windows temp dir, which only
// SYSTEM and Administrators may rename, under a DACL for those two alone: in
// the unelevated token Administrators is deny-only.
func createRuntimeDir() (string, error) {
	base, acl, err := runtimeBase()
	if err != nil {
		return "", err
	}
	if err := checkRuntimeBase(base, acl.user); err != nil {
		return "", err
	}
	return createProtectedDir(base, acl)
}

// runtimeBase picks the temp dir this run's directory goes in, and the rights
// it is made with. An elevated run never falls back to the user's temp dir: the
// point of its own base is that the user cannot reach it.
func runtimeBase() (string, runtimeACL, error) {
	if elevated() {
		win, err := windows.GetSystemWindowsDirectory()
		if err != nil {
			return "", runtimeACL{}, fmt.Errorf("каталог для рабочего конфига: %w", err)
		}
		acl, err := elevatedRuntimeACL()
		return filepath.Join(win, "Temp"), acl, err
	}
	// The temp dir may be reached through a junction or a symlink. The directory
	// is made where it really is, so the path that gets checked is that one.
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", runtimeACL{}, fmt.Errorf("каталог для рабочего конфига: %w", err)
	}
	acl, err := userRuntimeACL()
	return base, acl, err
}

// runtimeACL is the DACL a runtime dir is created with and then checked
// against: who must own it, and the only SIDs its entries may name.
type runtimeACL struct {
	sddl    string
	owner   *windows.SID
	allowed []*windows.SID
	// user is the SID the base may additionally belong to, or nil when nothing
	// unelevated is trusted with it.
	user *windows.SID
}

// elevatedRuntimeACL: owned by Administrators; full access for SYSTEM and
// Administrators, inherited by the config inside; nothing inherited from the
// parent (P).
func elevatedRuntimeACL() (runtimeACL, error) {
	system, admins, err := systemAndAdmins()
	if err != nil {
		return runtimeACL{}, err
	}
	return runtimeACL{
		sddl:    "O:BAD:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)",
		owner:   admins,
		allowed: []*windows.SID{system, admins},
	}, nil
}

// userRuntimeACL: owned by this user; full access for this user, SYSTEM and
// Administrators — an administrator can already reach anything this process
// can — and nothing at all for any other unprivileged user, whatever the temp
// dir above hands down.
func userRuntimeACL() (runtimeACL, error) {
	// From the process token: an environment string naming a user is not proof
	// of one.
	tu, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return runtimeACL{}, fmt.Errorf("SID текущего пользователя: %w", err)
	}
	user := tu.User.Sid
	system, admins, err := systemAndAdmins()
	if err != nil {
		return runtimeACL{}, err
	}
	return runtimeACL{
		sddl:    fmt.Sprintf("O:%[1]sD:P(A;OICI;FA;;;%[1]s)(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)", user),
		owner:   user,
		allowed: []*windows.SID{user, system, admins},
		user:    user,
	}, nil
}

func systemAndAdmins() (system, admins *windows.SID, err error) {
	if system, err = windows.CreateWellKnownSid(windows.WinLocalSystemSid); err != nil {
		return nil, nil, err
	}
	if admins, err = windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid); err != nil {
		return nil, nil, err
	}
	return system, admins, nil
}

// createProtectedDir creates a new directory in base with acl from the start —
// never for a moment under the parent's ACL — and reads back what the system
// actually set. A directory whose rights are not the ones asked for is removed
// again, before the config with the server's credentials is written into it.
func createProtectedDir(base string, acl runtimeACL) (string, error) {
	sd, err := windows.SecurityDescriptorFromString(acl.sddl)
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
		err = checkProtectedSD(got, acl)
	}
	if err != nil {
		_ = os.Remove(dir)
		return "", fmt.Errorf("права каталога для рабочего конфига %s: %w", dir, err)
	}
	return dir, nil
}

// checkProtectedSD confirms a descriptor is the one acl asked for: that owner,
// nothing inherited from the parent, and allow entries for those SIDs only. An
// entry of a kind this does not understand is not taken for a safe one.
func checkProtectedSD(sd *windows.SECURITY_DESCRIPTOR, acl runtimeACL) error {
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !acl.owner.Equals(owner) {
		return fmt.Errorf("владелец %s, а не %s", owner, acl.owner)
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
		if e.typ != windows.ACCESS_ALLOWED_ACE_TYPE || !sidIn(e.sid, acl.allowed) {
			return fmt.Errorf("в DACL есть запись типа %d для %s", e.typ, e.sid)
		}
	}
	return nil
}

func sidIn(sid *windows.SID, allowed []*windows.SID) bool {
	for _, a := range allowed {
		if a.Equals(sid) {
			return true
		}
	}
	return false
}

// checkTrustedDirs refuses a base that a process it does not trust could
// rename, replace or re-permission: it and every directory above it must be a
// plain directory, not a junction or symlink, owned by SYSTEM, Administrators,
// TrustedInstaller or user, and grant nobody else the right to delete it,
// delete its entries, or change its owner or DACL. Others may add entries of
// their own, as in the Windows temp dir, and read rights they hold there are
// not the runtime dir's concern: it is created with a DACL of its own. None of
// it can then change after the check.
//
// user is the SID an ordinary run trusts with its own temp dir. It is nil for
// an elevated run, which trusts nothing unelevated — that same user included.
func checkTrustedDirs(base string, user *windows.SID) error {
	for dir := base; ; dir = filepath.Dir(dir) {
		if err := checkTrustedDir(dir, user); err != nil {
			hint := ""
			if user != nil {
				hint = "; укажите в TMP каталог, менять который можете только вы и администраторы"
			}
			return fmt.Errorf("каталог для рабочего конфига %s ненадёжен: %s — %w%s", base, dir, err, hint)
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}

func checkTrustedDir(dir string, user *windows.SID) error {
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
	return checkTrustedSD(sd, user)
}

// fileDeleteChild is FILE_DELETE_CHILD, which x/sys does not name.
const fileDeleteChild = 0x40

// untrustedRights let their holder move a directory away, or give themselves
// the power to.
const untrustedRights = windows.DELETE | windows.WRITE_DAC | windows.WRITE_OWNER | fileDeleteChild | windows.GENERIC_ALL

const trustedInstallerSID = "S-1-5-80-956008885-3418522649-1831038044-1853292631-2271478464"

func trustedSID(sid *windows.SID, user *windows.SID) bool {
	return sid.IsWellKnown(windows.WinLocalSystemSid) ||
		sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) ||
		sid.String() == trustedInstallerSID ||
		(user != nil && user.Equals(sid))
}

// checkTrustedSD is checkTrustedDirs' rule for one directory's descriptor.
// Inherit-only entries do not apply to the directory itself and deny entries
// only take rights away; an entry of any other kind is not guessed at.
func checkTrustedSD(sd *windows.SECURITY_DESCRIPTOR, user *windows.SID) error {
	owner, _, err := sd.Owner()
	if err != nil {
		return err
	}
	if !trustedSID(owner, user) {
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
		case !trustedSID(e.sid, user) && e.mask&untrustedRights != 0:
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
