package updater

// installOwner sets no owner on Windows: there is no sudo handover to undo, and
// a new file inherits its directory's ACL.
func installOwner(_, _ string) (uid, gid int, ok bool) { return 0, 0, false }
