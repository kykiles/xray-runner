//go:build windows

package ipc

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// UsersGroup is the local group whose members may use the service, as the
// group xray-runner is on Linux. The install creates it and puts the user
// who installed in it; the uninstall deletes it.
const UsersGroup = "xray-runner Users"

// usersGroup is UsersGroup, a variable so tests can use a group of their own.
var usersGroup = UsersGroup

var (
	netapi32                    = windows.NewLazySystemDLL("netapi32.dll")
	procNetLocalGroupAdd        = netapi32.NewProc("NetLocalGroupAdd")
	procNetLocalGroupDel        = netapi32.NewProc("NetLocalGroupDel")
	procNetLocalGroupAddMembers = netapi32.NewProc("NetLocalGroupAddMembers")
	procNetLocalGroupGetMembers = netapi32.NewProc("NetLocalGroupGetMembers")
)

// Status codes of the NetLocalGroup calls that are not in x/sys.
const (
	nerrGroupExists   = 2223
	nerrGroupNotFound = 2220
	maxPreferredLen   = 0xFFFFFFFF
)

// netErr is a NET_API_STATUS as an error, nil for success.
func netErr(r uintptr) error {
	if r == 0 {
		return nil
	}
	return syscall.Errno(r)
}

// CreateUsersGroup creates UsersGroup; one already there is fine.
func CreateUsersGroup() error {
	name, err := windows.UTF16PtrFromString(usersGroup)
	if err != nil {
		return err
	}
	comment, err := windows.UTF16PtrFromString("Пользователи службы xray-runner: TUN, kill switch, маршрутизация по процессам")
	if err != nil {
		return err
	}
	info := struct{ name, comment *uint16 }{name, comment}                        // LOCALGROUP_INFO_1
	r, _, _ := procNetLocalGroupAdd.Call(0, 1, uintptr(unsafe.Pointer(&info)), 0) //nolint:gosec // G103: a pointer argument, converted in the call so it stays put
	err = netErr(r)
	if err == nil || errors.Is(err, windows.ERROR_ALIAS_EXISTS) || errors.Is(err, syscall.Errno(nerrGroupExists)) {
		return nil
	}
	return fmt.Errorf("группа %q не создана: %w", usersGroup, err)
}

// AddToUsersGroup puts sid in UsersGroup; a member already there is fine.
func AddToUsersGroup(sid *windows.SID) error {
	name, err := windows.UTF16PtrFromString(usersGroup)
	if err != nil {
		return err
	}
	member := struct{ sid *windows.SID }{sid}                                                                             // LOCALGROUP_MEMBERS_INFO_0
	r, _, _ := procNetLocalGroupAddMembers.Call(0, uintptr(unsafe.Pointer(name)), 0, uintptr(unsafe.Pointer(&member)), 1) //nolint:gosec // G103: a pointer argument, converted in the call so it stays put
	err = netErr(r)
	if err == nil || errors.Is(err, windows.ERROR_MEMBER_IN_ALIAS) {
		return nil
	}
	return fmt.Errorf("%s не добавлен в группу %q: %w", sid, usersGroup, err)
}

// DeleteUsersGroup deletes UsersGroup; one already gone is fine.
func DeleteUsersGroup() error {
	name, err := windows.UTF16PtrFromString(usersGroup)
	if err != nil {
		return err
	}
	r, _, _ := procNetLocalGroupDel.Call(0, uintptr(unsafe.Pointer(name))) //nolint:gosec // G103: a pointer argument, converted in the call so it stays put
	err = netErr(r)
	if err == nil || errors.Is(err, windows.ERROR_NO_SUCH_ALIAS) || errors.Is(err, syscall.Errno(nerrGroupNotFound)) {
		return nil
	}
	return fmt.Errorf("группа %q не удалена: %w", usersGroup, err)
}

// usersGroupSID is UsersGroup's SID, nil when there is no such group. The
// name is looked up in this computer's own accounts: a domain group of the
// same name is not the one the install made.
func usersGroupSID() *windows.SID {
	host, err := windows.ComputerName()
	if err != nil {
		return nil
	}
	sid, _, kind, err := windows.LookupSID("", host+`\`+usersGroup)
	if err != nil || kind != windows.SidTypeAlias {
		return nil
	}
	return sid
}

// directMember reports whether user is listed in UsersGroup itself. The
// token carries the groups as they were at logon; the list is as it is now,
// so a user the install or an administrator just added need not log in again.
func directMember(user *windows.SID) bool {
	name, err := windows.UTF16PtrFromString(usersGroup)
	if err != nil {
		return false
	}
	var buf *byte
	var read, total uint32
	r, _, _ := procNetLocalGroupGetMembers.Call(0, uintptr(unsafe.Pointer(name)), 0, //nolint:gosec // G103: pointer arguments, converted in the call so they stay put
		uintptr(unsafe.Pointer(&buf)), maxPreferredLen, //nolint:gosec // G103: as above
		uintptr(unsafe.Pointer(&read)), uintptr(unsafe.Pointer(&total)), 0) //nolint:gosec // G103: as above
	if buf != nil {
		defer func() { _ = windows.NetApiBufferFree(buf) }()
	}
	if netErr(r) != nil || buf == nil {
		return false
	}
	for _, m := range unsafe.Slice((**windows.SID)(unsafe.Pointer(buf)), read) { //nolint:gosec // G103: LOCALGROUP_MEMBERS_INFO_0s the call allocated
		if m != nil && m.Equals(user) {
			return true
		}
	}
	return false
}
