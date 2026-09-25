//go:build linux

package secret

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/zalando/go-keyring"
)

// On Linux the blob is sealed with AES-256-GCM, and the key lives in the
// Secret Service — GNOME Keyring, KWallet, KeePassXC — under the user's login.
// The key is made on first use; losing it (a wiped keyring) loses the sealed
// file, which the message then says.
const (
	service = "xray-runner"
	account = "subscriptions-key"
)

// keyTimeout bounds one trip to the Secret Service. An unlock may put a
// password prompt in front of the user, which takes a while; a bus with no
// service on it answers at once or not at all.
const keyTimeout = 60 * time.Second

var (
	keyOnce sync.Once
	key     []byte
	keyErr  error

	// Overridable in tests.
	keyringGet = keyring.Get
	keyringSet = keyring.Set
	fetchKey   = defaultFetchKey
)

// Default is the Secret Service sealer, or ErrUnavailable with the reason when
// there is none to reach — a headless server, a session without a keyring.
// The answer is worked out once per process.
func Default() (Sealer, error) {
	keyOnce.Do(func() { key, keyErr = fetchKey() })
	if keyErr != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, keyErr)
	}
	return gcmSealer{key: key}, nil
}

func defaultFetchKey() ([]byte, error) {
	if uid, gid, ok := sudoUser(); ok {
		return helperKey(uid, gid)
	}
	return withTimeout(localKey)
}

// localKey reads the key from this user's Secret Service, making it on first
// use. Read back after the write: two first runs at once each make a key, and
// the one that stays is the one both then use.
func localKey() ([]byte, error) {
	s, err := keyringGet(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		fresh := make([]byte, 32)
		if _, err := rand.Read(fresh); err != nil {
			return nil, err
		}
		if err := keyringSet(service, account, base64.StdEncoding.EncodeToString(fresh)); err != nil {
			return nil, fmt.Errorf("Secret Service: сохранить ключ: %w", err)
		}
		s, err = keyringGet(service, account)
	}
	if err != nil {
		return nil, fmt.Errorf("Secret Service: %w", err)
	}
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(k) != 32 {
		return nil, errors.New("Secret Service: ключ xray-runner повреждён")
	}
	return k, nil
}

func withTimeout(f func() ([]byte, error)) ([]byte, error) {
	type result struct {
		k   []byte
		err error
	}
	ch := make(chan result, 1)
	go func() {
		k, err := f()
		ch <- result{k, err}
	}()
	select {
	case r := <-ch:
		return r.k, r.err
	case <-time.After(keyTimeout):
		return nil, errors.New("Secret Service не ответил")
	}
}

// sudoUser is the user behind sudo, when this process is root because of it.
func sudoUser() (uid, gid int, ok bool) {
	if os.Geteuid() != 0 {
		return 0, 0, false
	}
	uid, err1 := strconv.Atoi(os.Getenv("SUDO_UID"))
	gid, err2 := strconv.Atoi(os.Getenv("SUDO_GID"))
	if err1 != nil || err2 != nil || uid == 0 {
		return 0, 0, false
	}
	return uid, gid, true
}

// helperKey asks the invoking user's Secret Service for the key, through this
// same program started as that user on their session bus. sudo drops the
// bus address from the environment, so it is put back from the systemd
// convention, /run/user/<uid>/bus; a session without it has no keyring to
// reach, and the subscriptions stay unsealed.
func helperKey(uid, gid int) ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	runtime := filepath.Join("/run/user", strconv.Itoa(uid))
	bus := filepath.Join(runtime, "bus")
	if _, err := os.Stat(bus); err != nil {
		return nil, fmt.Errorf("нет сессионной шины пользователя (%s)", bus)
	}
	env := []string{
		"DBUS_SESSION_BUS_ADDRESS=unix:path=" + bus,
		"XDG_RUNTIME_DIR=" + runtime,
		"PATH=/usr/local/bin:/usr/bin:/bin",
	}
	if u, err := user.LookupId(strconv.Itoa(uid)); err == nil {
		env = append(env, "HOME="+u.HomeDir, "USER="+u.Username, "LOGNAME="+u.Username)
	}
	ctx, cancel := context.WithTimeout(context.Background(), keyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, exe, HelperArg) //nolint:gosec // G204: this program itself, with a constant argument
	cmd.Env = env
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, errors.New(msg)
	}
	k, err := base64.StdEncoding.DecodeString(strings.TrimSpace(stdout.String()))
	if err != nil || len(k) != 32 {
		return nil, errors.New("помощник вернул неверный ключ")
	}
	return k, nil
}

// HelperMain is the helper side of helperKey: it prints the key of the user it
// runs as, and nothing else. It gives away nothing that user could not read
// from their own keyring.
func HelperMain() int {
	k, err := withTimeout(localKey)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(base64.StdEncoding.EncodeToString(k))
	return 0
}

type gcmSealer struct{ key []byte }

func (gcmSealer) Name() string { return "Secret Service" }

func (s gcmSealer) aead() (cipher.AEAD, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// additional binds the ciphertext to its purpose.
var additional = []byte("xray-runner subscriptions")

func (s gcmSealer) Seal(data []byte) ([]byte, error) {
	a, err := s.aead()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return wrap("aes-gcm", a.Seal(nonce, nonce, data, additional)), nil
}

func (s gcmSealer) Open(blob []byte) ([]byte, error) {
	payload, err := unwrap("aes-gcm", blob)
	if err != nil {
		return nil, err
	}
	a, err := s.aead()
	if err != nil {
		return nil, err
	}
	if len(payload) < a.NonceSize() {
		return nil, errors.New("файл обрезан")
	}
	out, err := a.Open(nil, payload[:a.NonceSize()], payload[a.NonceSize():], additional)
	if err != nil {
		return nil, errors.New("не расшифровывается: ключ в Secret Service не тот, которым файл зашифрован (связка ключей сброшена?), или файл повреждён")
	}
	return out, nil
}
