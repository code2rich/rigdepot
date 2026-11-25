// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package mcprun

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"sync"
)

// ProtocolVersion is the MCP version this package asks servers for. It also accepts the older
// ones listed in protocolVersions when a client asks for them.
const ProtocolVersion = "2025-06-18"

var protocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// message is a JSON-RPC 2.0 message: a request (ID and Method), a notification (Method only) or
// a response (ID and Result or Error).
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (m *message) isRequest() bool      { return m.Method != "" && len(m.ID) > 0 }
func (m *message) isNotification() bool { return m.Method != "" && len(m.ID) == 0 }
func (m *message) isResponse() bool     { return m.Method == "" && len(m.ID) > 0 }

// rpcError is the error of a JSON-RPC response.
type rpcError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return e.Message }

// The JSON-RPC error codes this package answers with.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
	// codeServer is the code of errors the server reports about itself, such as an upstream
	// that cannot be reached (the range -32000 to -32099).
	codeServer = -32000
)

func response(id json.RawMessage, result any) []byte {
	raw, err := json.Marshal(result)
	if err != nil {
		return errorResponse(id, codeInternal, err.Error())
	}
	data, _ := json.Marshal(message{JSONRPC: "2.0", ID: id, Result: raw})
	return data
}

func errorResponse(id json.RawMessage, code int, text string) []byte {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	data, _ := json.Marshal(message{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: text}})
	return data
}

// lineWriter writes one message per line, from any goroutine: the stdio transport of MCP.
type lineWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lineWriter) write(msg []byte) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	msg = bytes.TrimRight(msg, "\r\n")
	if _, err := l.w.Write(append(msg[:len(msg):len(msg)], '\n')); err != nil {
		return err
	}
	if f, ok := l.w.(interface{ Flush() error }); ok {
		return f.Flush()
	}
	return nil
}

// maxMessage is the longest line read as one message: tool results can be large.
const maxMessage = 64 << 20

// errTooLong: a line longer than maxMessage.
var errTooLong = errors.New("a message is longer than 64 MiB")

// readLines calls fn with every non-empty line of r, without its line break, until r ends.
func readLines(r io.Reader, fn func([]byte)) error {
	br := bufio.NewReaderSize(r, 64<<10)
	for {
		line, err := readLine(br)
		if len(bytes.TrimSpace(line)) > 0 {
			fn(line)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func readLine(br *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		chunk, err := br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > maxMessage {
			return nil, errTooLong
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return bytes.TrimRight(buf, "\r\n"), err
	}
}

// syncWriter makes a writer safe for the goroutines that share it: standard error gets the
// output of every stdio server and this package's own notes.
type syncWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (s *syncWriter) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.w.Write(p)
}

// shared returns o with Stderr safe for concurrent use.
func (o Options) shared() Options {
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if _, ok := o.Stderr.(*syncWriter); !ok {
		o.Stderr = &syncWriter{w: o.Stderr}
	}
	return o
}
