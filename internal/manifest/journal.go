// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package manifest

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/code2rich/rigdepot/internal/home"
)

// IntentKind says what an intent announces.
type IntentKind string

const (
	// Write announces that a target is about to be written. Done, its Entry is recorded.
	Write IntentKind = "write"
	// Remove announces that a target is about to be removed. Done, its Entry is dropped.
	Remove IntentKind = "remove"
)

// Intent is one line of the intent log: a change that was started and is not yet recorded in
// the manifest.
//
// Before and After are the target's digest before and after the change, or "" for "does not
// exist". They are what recovery compares the target with.
type Intent struct {
	// ID is unique per intent. Intend fills it in. It is short and made of letters and digits,
	// so the executor uses it in the names of its temporary files.
	ID     string     `json:"id"`
	Kind   IntentKind `json:"kind"`
	Entry  Entry      `json:"entry"`
	Before string     `json:"before"`
	After  string     `json:"after"`
	At     time.Time  `json:"at"`
}

// Intend appends in to the intent log and flushes it to disk before returning, so that the
// change it announces can start. It fills in ID and At when they are empty and returns the
// intent as logged.
func (l *Locked) Intend(in Intent) (Intent, error) {
	if err := l.held(); err != nil {
		return Intent{}, err
	}
	if in.Kind != Write && in.Kind != Remove {
		return Intent{}, fmt.Errorf("manifest: intent of unknown kind %q", in.Kind)
	}
	if in.Entry.ID == "" {
		return Intent{}, errors.New("manifest: intent without an entry id")
	}
	if in.ID == "" {
		var b [6]byte
		if _, err := rand.Read(b[:]); err != nil {
			return Intent{}, err
		}
		in.ID = hex.EncodeToString(b[:])
	}
	if in.At.IsZero() {
		in.At = time.Now().UTC()
	}
	line, err := json.Marshal(in)
	if err != nil {
		return Intent{}, err
	}
	path := l.s.dir.Join(home.JournalFile)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, home.FilePerm)
	if err != nil {
		return Intent{}, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return Intent{}, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return Intent{}, err
	}
	return in, f.Close()
}

// Commit records what in announced, in the manifest, and then clears it from the log: for
// Write the entry is put (replacing one with the same ID), for Remove it is dropped. If the
// process dies between the two steps, recovery finds the target in its final state and
// commits again, which changes nothing.
func (l *Locked) Commit(in Intent) error {
	err := l.update(func(m *Manifest) {
		if in.Kind == Remove {
			m.Delete(in.Entry.ID)
		} else {
			m.Put(in.Entry)
		}
	})
	if err != nil {
		return err
	}
	return l.Forget(in.ID)
}

// Forget clears an intent from the log without recording anything, for a change that was
// rolled back or never started.
func (l *Locked) Forget(id string) error {
	pending, err := l.Pending()
	if err != nil {
		return err
	}
	var keep []Intent
	for _, in := range pending {
		if in.ID != id {
			keep = append(keep, in)
		}
	}
	return l.writeJournal(keep)
}

func (l *Locked) writeJournal(intents []Intent) error {
	path := l.s.dir.Join(home.JournalFile)
	if len(intents) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	var b bytes.Buffer
	for _, in := range intents {
		line, err := json.Marshal(in)
		if err != nil {
			return err
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return home.WriteFileAtomic(path, b.Bytes(), home.FilePerm)
}

// Pending returns the intents in the log, oldest first.
//
// A last line without its newline that does not parse is a write of the log that was cut off
// by a crash; the change it would have announced never started, so it is ignored. Any other
// line that does not parse makes the log, and with it the local state, unknown.
func (l *Locked) Pending() ([]Intent, error) {
	if err := l.held(); err != nil {
		return nil, err
	}
	path := l.s.dir.Join(home.JournalFile)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, unknown(path, Unreadable, err)
	}
	var out []Intent
	lines := bytes.Split(data, []byte("\n"))
	for i, line := range lines {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var in Intent
		if err := json.Unmarshal(line, &in); err != nil || in.ID == "" || in.Entry.ID == "" ||
			(in.Kind != Write && in.Kind != Remove) {
			if i == len(lines)-1 {
				continue // cut off: the last line has no newline after it
			}
			if err == nil {
				err = errors.New("an intent lacks its id, entry or kind")
			}
			return nil, unknown(path, Malformed, fmt.Errorf("line %d: %w", i+1, err))
		}
		out = append(out, in)
	}
	return out, nil
}

// Observation is what a target looks like now, as far as recovery needs to know.
type Observation struct {
	// Exists is false when there is nothing at the target.
	Exists bool
	// Digest is the target's digest when it exists.
	Digest string
	// Err is set when the target could not be looked at. The intent is then left alone.
	Err error
}

func (o Observation) state() string {
	if !o.Exists {
		return ""
	}
	return o.Digest
}

// Recovery is what Recover did with the intents it found.
type Recovery struct {
	// Committed are the intents whose change was complete: recorded now.
	Committed []Intent
	// Dropped are the intents whose change never happened or was rolled back: forgotten.
	Dropped []Intent
	// Unresolved are the intents whose target is in neither state, or could not be looked at.
	// They stay in the log, and the doctor command lists them for a person to look at.
	Unresolved []Intent
}

// Recover settles the intents left in the log by a program that did not finish. For each, it
// asks observe what the target looks like now:
//
//   - in the state the intent announced (After): the change is complete and is committed;
//   - in the state it was in before (Before): the change never happened and is forgotten;
//   - anything else, or unreadable: the intent stays, and Recover reports it.
//
// observe may tidy up what an interrupted change left behind (the executor puts a directory it
// had moved aside back in place) before it looks.
func (l *Locked) Recover(observe func(Intent) Observation) (Recovery, error) {
	var r Recovery
	pending, err := l.Pending()
	if err != nil {
		return r, err
	}
	for _, in := range pending {
		o := observe(in)
		switch {
		case o.Err != nil:
			r.Unresolved = append(r.Unresolved, in)
		case o.state() == in.After:
			if err := l.Commit(in); err != nil {
				return r, err
			}
			r.Committed = append(r.Committed, in)
		case o.state() == in.Before:
			if err := l.Forget(in.ID); err != nil {
				return r, err
			}
			r.Dropped = append(r.Dropped, in)
		default:
			r.Unresolved = append(r.Unresolved, in)
		}
	}
	return r, nil
}
