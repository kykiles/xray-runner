// Package ipc is the channel between the interface, which runs as the user,
// and the service, which holds the right to change the network (H10).
//
// A message is a JSON object behind a four-byte big-endian length. Every
// message carries the protocol version; the first one on a connection must be
// a hello, and a peer speaking another version is refused. Requests carry an
// id, and the reply to one carries the same id; events carry none and flow
// from the service to the interface on their own.
//
// The service takes structured data only: the pieces of a core config the
// interface may choose (outbounds, routing, dns — see xraycfg.ClientParts) and
// a handful of switches. Anything that names a file or a program — log paths,
// the api, the core itself — is the service's own, so no request can have it
// write or run something with its rights.
package ipc

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"xray-runner/internal/system"
	"xray-runner/internal/xraycfg"
)

// Version is the protocol spoken here. A change that an older peer would
// misread bumps it.
const Version = 1

// MaxMessage bounds one message in either direction. A panel profile with a
// few dozen servers is some hundred kilobytes; anything near this is not one.
const MaxMessage = 4 << 20

// Message types. The first seven are requests; reply answers one of them, and
// event is the service speaking on its own.
const (
	TypeHello        = "hello"
	TypeStartTun     = "start_tun"
	TypeStop         = "stop"
	TypeStatus       = "status"
	TypeEnableSplit  = "enable_split"
	TypeRefreshSplit = "refresh_split"
	TypeDisableSplit = "disable_split"
	TypeCloseConns   = "close_conns"
	TypeReply        = "reply"
	TypeEvent        = "event"
)

// Message is one frame on the wire.
type Message struct {
	V     int             `json:"v"`
	ID    uint64          `json:"id,omitempty"`
	Type  string          `json:"type"`
	Body  json.RawMessage `json:"body,omitempty"`
	Error string          `json:"error,omitempty"`
}

// Hello opens a connection. Resume names a session this client held on a
// connection that dropped; the service hands it back if it is still there and
// belongs to the same user.
type Hello struct {
	Version int    `json:"version"`
	Resume  string `json:"resume,omitempty"`
}

// HelloReply says who answered.
type HelloReply struct {
	Version        int    `json:"version"`
	ServiceVersion string `json:"service_version"`
	// CoreVersion is the first line of the service's `xray version`, empty
	// when it could not be read.
	CoreVersion string `json:"core_version,omitempty"`
	// Resumed is set when the session named in Hello.Resume is this
	// connection's again.
	Resumed bool `json:"resumed,omitempty"`
}

// StartTun asks for a tun session: the core run by the service in TUN mode,
// the routes, and the kill switch when asked for. The reply comes once the
// first core is set up, or with the reason it was not.
type StartTun struct {
	Parts xraycfg.ClientParts `json:"parts"`
	// LogLevel is the core's log level, one of xraycfg.LogLevels.
	LogLevel      string `json:"log_level"`
	AllowInsecure bool   `json:"allow_insecure"`
	KillSwitch    bool   `json:"kill_switch"`
	// Split marks a session that routes only listed processes through the
	// tunnel (Windows: xray matches the process itself). The kill switch has
	// no place in one.
	Split bool `json:"split"`
	// CheckURLs are what the health check fetches through the tunnel; http
	// and https only.
	CheckURLs []string `json:"check_urls"`
	// Title names the session in the service's log and status.
	Title string `json:"title,omitempty"`
}

// StartReply hands back the token that resumes the session over another
// connection.
type StartReply struct {
	Token string `json:"token"`
}

// SplitRequest names the processes to route through the tunnel (Linux: the
// cgroup and the nft redirect into the interface's own core). Only the
// caller's own processes are moved.
type SplitRequest struct {
	Names []string `json:"names"`
}

// SplitReply is what the scan found: the listed names that run, and those
// whose connections from before the move could not be closed.
type SplitReply struct {
	Token    string   `json:"token,omitempty"`
	Matched  []string `json:"matched"`
	Unclosed []string `json:"unclosed,omitempty"`
	// Moved names the processes just moved in, pid → name. Their
	// connections from before the move still go past the tunnel; the
	// service cannot see which sockets are theirs, the user can, and asks
	// for them to be closed with CloseConns.
	Moved map[string]string `json:"moved,omitempty"`
}

// CloseConns asks the service to close connections of the caller's that
// went past the tunnel before their process joined the split. Each one is
// checked to be the caller's socket before it is closed.
type CloseConns struct {
	Conns []system.Conn `json:"conns"`
}

// CloseConnsReply lists the connections still open afterwards.
type CloseConnsReply struct {
	Open []system.Conn `json:"open,omitempty"`
}

// StatusReply describes the service's session, if any.
type StatusReply struct {
	Active bool   `json:"active"`
	Kind   string `json:"kind,omitempty"` // "tun" or "split"
	Title  string `json:"title,omitempty"`
	// Mine is set when the session belongs to the asking user.
	Mine bool `json:"mine,omitempty"`
	// Attached is set when a connection holds the session; false while it
	// waits out the grace period after its connection dropped.
	Attached bool `json:"attached,omitempty"`
}

// Event kinds.
const (
	EventLog    = "log"    // a line of the service's log for this session
	EventStatus = "status" // a status-screen update
	EventEnded  = "ended"  // the session ended on its own; Error says why
)

// Event is what the service says unasked.
type Event struct {
	Kind  string `json:"kind"`
	Level string `json:"level,omitempty"`
	Line  string `json:"line,omitempty"`
	// A status update, the fields of tui.StatusUpdate.
	OK          bool   `json:"ok,omitempty"`
	LatencyMs   int64  `json:"latency_ms,omitempty"`
	Note        string `json:"note,omitempty"`
	Err         bool   `json:"err,omitempty"`
	ResetHealth bool   `json:"reset_health,omitempty"`
	Generation  int    `json:"generation,omitempty"`
	Error       string `json:"error,omitempty"`
}

// ErrTooLarge is a frame over MaxMessage, or an empty one.
var ErrTooLarge = errors.New("ipc: сообщение пустое или больше предела")

// WriteMessage sends one frame.
func WriteMessage(w io.Writer, m Message) error {
	m.V = Version
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if len(data) > MaxMessage {
		return ErrTooLarge
	}
	buf := make([]byte, 4+len(data))
	binary.BigEndian.PutUint32(buf, uint32(len(data))) //nolint:gosec // G115: bounded by MaxMessage above
	copy(buf[4:], data)
	_, err = w.Write(buf)
	return err
}

// ReadMessage reads one frame. A frame of another protocol version is an
// error, as is one over MaxMessage: nothing past the length is read then, and
// the connection is of no further use.
func ReadMessage(r io.Reader) (Message, error) { return readMessage(r, MaxMessage) }

// readMessage is ReadMessage with a limit of its own. The buffer grows with
// what arrives rather than with what the length promises: a peer that
// announces a large frame and sends nothing gets nothing set aside for it.
func readMessage(r io.Reader, limit uint32) (Message, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return Message{}, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n == 0 || n > limit {
		return Message{}, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(r, int64(n)))
	if err != nil {
		return Message{}, err
	}
	if len(data) < int(n) {
		return Message{}, io.ErrUnexpectedEOF
	}
	var m Message
	if err := json.Unmarshal(data, &m); err != nil {
		return Message{}, fmt.Errorf("ipc: сообщение не разобрано: %w", err)
	}
	if m.V != Version {
		return m, &VersionError{Got: m.V}
	}
	return m, nil
}

// VersionError is a peer speaking another protocol version.
type VersionError struct{ Got int }

func (e *VersionError) Error() string {
	return fmt.Sprintf("ipc: версия протокола %d, ожидалась %d — обновите программу и службу вместе", e.Got, Version)
}
