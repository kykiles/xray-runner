// Package safefile reads and writes the app's own state files — apps.txt,
// last_server.json, subscriptions.txt, hwid — without following a symlink found
// in their place. In TUN mode the tool runs as root, while that state lives in a
// directory the invoking user owns (~/.config/xray-runner under sudo), so a link
// left there must not steer root's reads or writes to another file (G02).
//
// The guard is a unix, root-under-sudo concern; on Windows, where creating a
// symlink is itself privileged, reads are plain and writes only keep the
// replace-through-a-temp-file behaviour.
package safefile

import "os"

// ReadFile returns the file's contents, refusing to read through a symlink at
// the final path component or anything but a regular file. A missing file
// reports fs.ErrNotExist, as os.ReadFile does, so callers that treat "no file"
// as empty keep working.
func ReadFile(path string) ([]byte, error) {
	return readFile(path)
}

// WriteFile replaces path through a temp file created next to it, so whatever
// sits at path — a symlink included — is replaced rather than written through,
// and a crash leaves the previous contents rather than a truncated file. Under
// sudo the new file is handed to the invoking user by descriptor before it is
// published, so a later run without sudo can still read it.
func WriteFile(path string, data []byte, perm os.FileMode) error {
	return writeFile(path, data, perm)
}
