// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cliout

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/code2rich/rigdepot/internal/brand"
)

// Verbosity is how much goes to standard error besides results and errors.
type Verbosity int

const (
	Quiet   Verbosity = -1 // only warnings and errors
	Normal  Verbosity = 0
	Verbose Verbosity = 1 // debug lines too
)

// Options decide how an Output renders.
type Options struct {
	JSON      bool // machine-readable: exactly one JSON document on standard output
	Color     bool // ANSI colours on human output; see ColorEnabled
	Verbosity Verbosity
}

// Output is where a command writes. Results go to standard output and
// everything else (progress, hints, warnings, errors in human mode) to
// standard error, so that "rgd status > file" gets a clean result.
//
// With --json, standard output carries exactly one JSON document: the result
// written by Result, or the error document written by Fail. Output enforces
// it: a second document is refused.
type Output struct {
	stdout, stderr io.Writer
	opts           Options
	done           bool
}

// New returns an Output on the given streams.
func New(stdout, stderr io.Writer, opts Options) *Output {
	return &Output{stdout: stdout, stderr: stderr, opts: opts}
}

// JSON reports whether the output is machine-readable.
func (o *Output) JSON() bool { return o.opts.JSON }

// Options returns the options the output renders with.
func (o *Output) Options() Options { return o.opts }

// Done reports whether the result has been written.
func (o *Output) Done() bool { return o.done }

// Stderr is the stream for progress and hints. Commands rarely need it
// directly; Info, Warn and Debug cover the usual cases.
func (o *Output) Stderr() io.Writer { return o.stderr }

// Schema returns the name of a JSON output schema, such as "rgd.version/v1".
// Each kind of output has its own name and version; fields are only ever
// added, and a change of meaning gets a new version.
func Schema(kind string, version int) string {
	return fmt.Sprintf("%s.%s/v%d", brand.CLI, kind, version)
}

// ErrResultWritten is returned by Result when the command already wrote its
// result: a command has exactly one.
var ErrResultWritten = errors.New("cliout: the result has already been written")

// Result writes the command's result. With --json, data (a struct or map
// that encodes as a JSON object without a "schema" field) is written as one
// JSON document whose first field is "schema"; otherwise human writes the
// result for people. A command calls Result once, at the end.
func (o *Output) Result(schema string, data any, human func(w io.Writer) error) error {
	if o.done {
		return ErrResultWritten
	}
	if !o.opts.JSON {
		o.done = true
		return human(o.stdout)
	}
	doc, err := Document(schema, data)
	if err != nil {
		return err
	}
	o.done = true
	_, err = o.stdout.Write(doc)
	return err
}

// Document encodes data as a JSON object with "schema" as its first field,
// followed by a newline. Keys are English and values are not translated.
func Document(schema string, data any) ([]byte, error) {
	var body bytes.Buffer
	enc := json.NewEncoder(&body)
	enc.SetEscapeHTML(false)
	if data == nil {
		data = struct{}{}
	}
	if err := enc.Encode(data); err != nil {
		return nil, err
	}
	b := bytes.TrimSpace(body.Bytes())
	if len(b) < 2 || b[0] != '{' {
		return nil, fmt.Errorf("cliout: the result for %s is not a JSON object", schema)
	}
	var probe map[string]json.RawMessage
	if err := json.Unmarshal(b, &probe); err != nil {
		return nil, err
	}
	if _, ok := probe["schema"]; ok {
		return nil, fmt.Errorf("cliout: the result for %s has its own schema field", schema)
	}
	name, _ := json.Marshal(schema)
	var doc bytes.Buffer
	doc.WriteString(`{"schema":`)
	doc.Write(name)
	if string(b) != "{}" {
		doc.WriteByte(',')
		doc.Write(b[1:])
	} else {
		doc.WriteByte('}')
	}
	doc.WriteByte('\n')
	return doc.Bytes(), nil
}

// errorDoc is the JSON body of a failure.
type errorDoc struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Exit    int    `json:"exit"`
	} `json:"error"`
}

// Fail reports the error a command ended with and returns the exit code.
// With --json and no result written yet, standard output gets the error
// document {"schema":"rgd.error/v1","error":{"code","message","exit"}};
// otherwise the message, and the hint if there is one, go to standard error
// after the program's name. A Status has already been reported by the result
// and prints nothing.
func (o *Output) Fail(err error) ExitCode {
	code := ExitOf(err)
	if err == nil {
		return code
	}
	var status Status
	if errors.As(err, &status) {
		return code
	}
	var e *Error
	if !errors.As(err, &e) {
		e = &Error{Exit: ExitFailure, Code: "internal", Message: err.Error()}
	}
	if o.opts.JSON && !o.done {
		var d errorDoc
		d.Error.Code, d.Error.Message, d.Error.Exit = e.Code, e.Message, int(e.Exit)
		if doc, derr := Document(Schema("error", 1), d); derr == nil {
			o.done = true
			o.stdout.Write(doc)
			return code
		}
	}
	fmt.Fprintf(o.stderr, "%s: %s\n", brand.CLI, o.Paint(Red, e.Message))
	if e.Hint != "" {
		fmt.Fprintln(o.stderr, e.Hint)
	}
	return code
}

// Info writes a line for people to standard error, unless quiet.
func (o *Output) Info(line string) {
	if o.opts.Verbosity >= Normal {
		fmt.Fprintln(o.stderr, line)
	}
}

// Warn writes a warning to standard error, even when quiet.
func (o *Output) Warn(line string) {
	fmt.Fprintln(o.stderr, o.Paint(Yellow, line))
}

// Debug writes a line to standard error only with -v.
func (o *Output) Debug(line string) {
	if o.opts.Verbosity >= Verbose {
		fmt.Fprintln(o.stderr, line)
	}
}

// Style is an ANSI colour or attribute.
type Style string

const (
	Bold   Style = "1"
	Dim    Style = "2"
	Red    Style = "31"
	Green  Style = "32"
	Yellow Style = "33"
	Cyan   Style = "36"
)

// Paint wraps s in the style when colours are on, and returns it unchanged
// otherwise.
func (o *Output) Paint(style Style, s string) string {
	if !o.opts.Color || o.opts.JSON || s == "" {
		return s
	}
	return "\x1b[" + string(style) + "m" + s + "\x1b[0m"
}

// ColorEnabled decides whether human output gets colours: only when the
// stream is a terminal and neither --no-color nor NO_COLOR / RGD_NO_COLOR
// (already folded into noColor) asks otherwise. JSON is never coloured.
func ColorEnabled(noColor, json, terminal bool) bool {
	return terminal && !noColor && !json
}
