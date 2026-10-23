package subscription

// The last good answer of every subscription is kept on disk, so a panel that
// times out or is down does not lock the user out of servers that may well
// still work: the menu opens the copy instead, and says how old it is.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"xray-runner/internal/config"
	"xray-runner/internal/safefile"
	"xray-runner/internal/secret"
)

// Stale says the profiles came from the saved copy, not from the panel.
type Stale struct {
	At  time.Time // when the copy was fetched
	Err error     // why the fetch just failed
}

// cachedAnswer is what the cache keeps: the body as the panel sent it, parsed
// again on the way out so the copy follows the parser of the running version.
type cachedAnswer struct {
	At   time.Time `json:"at"`
	Body []byte    `json:"body"`
	Info PanelInfo `json:"info"`
}

// cacheDir is where the copies live. Overridable in tests.
var cacheDir = func() (string, error) { return config.CacheSubdir("subs") }

// FetchProfilesCached is FetchProfiles that falls back on the last good answer
// when the fetch fails. stale is nil when the profiles are fresh. A fetch the
// caller stopped is not the panel's failure and returns its error as is.
func FetchProfilesCached(ctx context.Context, rawURL string, opts ...FetchOption) (profiles []Profile, info PanelInfo, stale *Stale, err error) {
	profiles, info, body, err := fetchProfiles(ctx, rawURL, opts...)
	if err == nil {
		if body != nil {
			if err := saveCached(rawURL, body, info); err != nil {
				slog.Warn("копия подписки не сохранена", "error", err)
			}
		}
		return profiles, info, nil, nil
	}
	if ctx.Err() != nil {
		return nil, PanelInfo{}, nil, err
	}
	c, cerr := loadCached(rawURL)
	if cerr != nil {
		if !errors.Is(cerr, fs.ErrNotExist) {
			slog.Warn("копия подписки не читается", "error", cerr)
		}
		return nil, PanelInfo{}, nil, err
	}
	profiles, perr := parseProfiles(c.Body)
	if perr != nil {
		slog.Warn("копия подписки не разбирается", "error", perr)
		return nil, PanelInfo{}, nil, err
	}
	return profiles, c.Info, &Stale{At: c.At, Err: err}, nil
}

// cachePath names the copy by a fingerprint of the URL: the URL carries the
// panel's token and has no business in a file name.
func cachePath(rawURL string) (string, error) {
	dir, err := cacheDir()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(rawURL))
	return filepath.Join(dir, hex.EncodeToString(sum[:8])), nil
}

// saveCached writes the copy, sealed the way the list is: it holds every
// server's credentials. Without a key store it stays plain, 0600, as the list
// does.
func saveCached(rawURL string, body []byte, info PanelInfo) error {
	path, err := cachePath(rawURL)
	if err != nil {
		return err
	}
	data, err := json.Marshal(cachedAnswer{At: time.Now(), Body: body, Info: info})
	if err != nil {
		return err
	}
	if s, err := sealer(); err == nil {
		if data, err = s.Seal(data); err != nil {
			return err
		}
	}
	return safefile.WriteFile(path, data, 0600)
}

func loadCached(rawURL string) (cachedAnswer, error) {
	var c cachedAnswer
	path, err := cachePath(rawURL)
	if err != nil {
		return c, err
	}
	data, err := safefile.ReadFile(path)
	if err != nil {
		return c, err
	}
	if secret.Sealed(data) {
		s, err := sealer()
		if err != nil {
			return c, fmt.Errorf("копия зашифрована, но ключ недоступен: %w", err)
		}
		if data, err = s.Open(data); err != nil {
			return c, err
		}
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return c, err
	}
	return c, nil
}

// removeCached drops the copy of a subscription the user removed: its
// credentials should not outlive it.
func removeCached(rawURL string) {
	path, err := cachePath(rawURL)
	if err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("копия удалённой подписки не стёрта", "error", err)
	}
}
