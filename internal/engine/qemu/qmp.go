package qemu

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"
)

// QMP, QEMU's control protocol (ADR-025). QEMU accepts one client at a time, so a monitor connection
// is opened per operation, under the VM's lock, and always closed.

type qmpConn struct {
	conn net.Conn
	r    *bufio.Reader
}

type qmpMessage struct {
	Return json.RawMessage `json:"return"`
	Error  *struct {
		Class string `json:"class"`
		Desc  string `json:"desc"`
	} `json:"error"`
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// dialQMP connects and leaves the monitor in command mode.
func dialQMP(ctx context.Context, socket string) (*qmpConn, error) {
	d := net.Dialer{Timeout: commandTimeout}
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, fmt.Errorf("connect to the VM's monitor: %w", err)
	}
	q := &qmpConn{conn: conn, r: bufio.NewReaderSize(conn, 64<<10)}
	if _, err := q.read(ctx); err != nil { // greeting
		conn.Close()
		return nil, err
	}
	if _, err := q.execute(ctx, "qmp_capabilities", nil); err != nil {
		conn.Close()
		return nil, err
	}
	return q, nil
}

func (q *qmpConn) Close() error { return q.conn.Close() }

func (q *qmpConn) read(ctx context.Context) (qmpMessage, error) {
	deadline := time.Now().Add(commandTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = q.conn.SetReadDeadline(deadline)
	line, err := q.r.ReadBytes('\n')
	if err != nil {
		return qmpMessage{}, fmt.Errorf("read from the VM's monitor: %w", err)
	}
	var m qmpMessage
	if err := json.Unmarshal(line, &m); err != nil {
		return qmpMessage{}, fmt.Errorf("the VM's monitor sent %q: %w", line, err)
	}
	return m, nil
}

// execute runs one command and returns its result, skipping events.
func (q *qmpConn) execute(ctx context.Context, command string, args any) (json.RawMessage, error) {
	req := map[string]any{"execute": command}
	if args != nil {
		req["arguments"] = args
	}
	b, _ := json.Marshal(req)
	_ = q.conn.SetWriteDeadline(time.Now().Add(commandTimeout))
	if _, err := q.conn.Write(append(b, '\n')); err != nil {
		return nil, fmt.Errorf("write to the VM's monitor: %w", err)
	}
	for {
		m, err := q.read(ctx)
		if err != nil {
			return nil, err
		}
		if m.Event != "" {
			continue
		}
		if m.Error != nil {
			return nil, fmt.Errorf("%s: %s", command, m.Error.Desc)
		}
		return m.Return, nil
	}
}

// waitEvent reads until an event named in names arrives, or ctx ends. Reads time out per message, so
// a long wait polls.
func (q *qmpConn) waitEvent(ctx context.Context, names ...string) (qmpMessage, error) {
	for {
		if err := ctx.Err(); err != nil {
			return qmpMessage{}, err
		}
		m, err := q.read(ctx)
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			continue
		}
		if err != nil {
			return qmpMessage{}, err
		}
		for _, n := range names {
			if m.Event == n {
				return m, nil
			}
		}
	}
}

// qmpStatus is query-status's answer: "running", "shutdown", "paused", …
func qmpStatus(ctx context.Context, socket string) (string, error) {
	q, err := dialQMP(ctx, socket)
	if err != nil {
		return "", err
	}
	defer q.Close()
	raw, err := q.execute(ctx, "query-status", nil)
	if err != nil {
		return "", err
	}
	var st struct {
		Status string `json:"status"`
	}
	err = json.Unmarshal(raw, &st)
	return st.Status, err
}

// qmpRun runs one command on a fresh connection.
func qmpRun(ctx context.Context, socket, command string, args any) error {
	q, err := dialQMP(ctx, socket)
	if err != nil {
		return err
	}
	defer q.Close()
	_, err = q.execute(ctx, command, args)
	return err
}
