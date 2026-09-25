package ipc

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"net"
	"testing"
	"time"
)

func TestMessageRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteMessage(&buf, Message{ID: 7, Type: TypeStatus, Body: []byte(`{"a":1}`)}); err != nil {
		t.Fatal(err)
	}
	m, err := ReadMessage(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if m.V != Version || m.ID != 7 || m.Type != TypeStatus || string(m.Body) != `{"a":1}` {
		t.Fatalf("got %+v", m)
	}
}

// A length past MaxMessage is refused before anything is read or allocated.
func TestReadMessageRefusesOversize(t *testing.T) {
	for _, n := range []uint32{0, MaxMessage + 1, 1 << 31} {
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], n)
		if _, err := ReadMessage(bytes.NewReader(hdr[:])); !errors.Is(err, ErrTooLarge) {
			t.Errorf("length %d: err = %v, want ErrTooLarge", n, err)
		}
	}
}

func TestWriteMessageRefusesOversize(t *testing.T) {
	big := make([]byte, MaxMessage)
	for i := range big {
		big[i] = 'a'
	}
	body := append(append([]byte(`"`), big...), '"')
	if err := WriteMessage(&bytes.Buffer{}, Message{Type: TypeStatus, Body: body}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
}

func TestReadMessageRefusesOtherVersion(t *testing.T) {
	data := []byte(`{"v":2,"type":"hello"}`)
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.BigEndian, uint32(len(data)))
	buf.Write(data)
	_, err := ReadMessage(&buf)
	var ve *VersionError
	if !errors.As(err, &ve) || ve.Got != 2 {
		t.Fatalf("err = %v, want VersionError{2}", err)
	}
}

// A reply goes to the call with its id, whatever order they come back in, and
// events arrive on their own channel.
func TestClientCallsAndEvents(t *testing.T) {
	cli, srv := net.Pipe()
	c := NewClient(cli)
	defer func() { _ = c.Close() }()
	conn := NewConn(srv, Peer{Key: "k"})
	defer conn.Close()

	// Both requests are read before either is answered, and the answers go
	// back in the other order than the requests came.
	go func() {
		m1, _ := conn.Read()
		m2, _ := conn.Read()
		conn.Event(Event{Kind: EventStatus, Note: "hi"})
		answer := func(m Message) {
			if m.Type == TypeStatus {
				conn.Reply(m.ID, StatusReply{Kind: "second"}, nil)
			} else {
				conn.Reply(m.ID, nil, errors.New("refused"))
			}
		}
		answer(m2)
		answer(m1)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- c.Call(ctx, TypeStop, struct{}{}, nil) }()
	var st StatusReply
	if err := c.Call(ctx, TypeStatus, struct{}{}, &st); err != nil || st.Kind != "second" {
		t.Fatalf("second call: %+v, %v", st, err)
	}
	var re *RemoteError
	if err := <-errc; !errors.As(err, &re) || re.Msg != "refused" {
		t.Fatalf("first call: %v", err)
	}
	select {
	case ev := <-c.Events():
		if ev.Note != "hi" {
			t.Fatalf("event %+v", ev)
		}
	case <-ctx.Done():
		t.Fatal("no event")
	}
}

// A connection that goes fails the calls waiting on it instead of leaving
// them hanging.
func TestClientCallFailsWhenClosed(t *testing.T) {
	cli, srv := net.Pipe()
	c := NewClient(cli)
	go func() {
		_, _ = ReadMessage(srv)
		_ = srv.Close()
	}()
	err := c.Call(context.Background(), TypeStatus, struct{}{}, nil)
	if !errors.Is(err, ErrClosed) {
		t.Fatalf("err = %v, want ErrClosed", err)
	}
	<-c.Done()
}
