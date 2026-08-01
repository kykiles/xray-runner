//go:build bundle

package bundle

import (
	"embed"
	"io/fs"
)

// assets/ is filled by the build (see Taskfile: bundle:assets) and is not in git —
// the core and its databases are downloaded separately.
//
//go:embed assets
var embedded embed.FS

func init() {
	sub, err := fs.Sub(embedded, "assets")
	if err != nil {
		panic(err) // the directory is embedded above; its absence is a build error
	}
	assets = sub
}
