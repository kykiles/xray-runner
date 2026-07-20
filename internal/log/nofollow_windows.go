package log

// Windows has no O_NOFOLLOW. The attack it guards against needs a root process
// opening a user-writable path, which is the unix sudo scenario; creating
// symlinks on Windows is itself privileged.
const openNoFollow = 0
