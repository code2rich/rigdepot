// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cliout

import (
	"errors"
	"strconv"
)

// ExitCode is the exit status of the command-line program. Scripts and the
// automatic sync decide what to do next by it, so the numbers are fixed: a
// code is only ever added, never changed or reused.
type ExitCode int

const (
	// ExitOK: success, nothing needs the person's attention.
	ExitOK ExitCode = 0
	// ExitFailure: something failed, such as a write or an internal error.
	ExitFailure ExitCode = 1
	// ExitUsage: wrong arguments or an unknown command.
	ExitUsage ExitCode = 2
	// ExitPending: everything that could be done was done; something waits
	// for the person (a global change not agreed to, a resource changed
	// locally and not overwritten, a missing personal secret).
	ExitPending ExitCode = 3
	// ExitCheckFailed: a check found a must-fix problem.
	ExitCheckFailed ExitCode = 4
	// ExitDiff: there are differences (sync --plan --exit-code, status
	// --exit-code).
	ExitDiff ExitCode = 5
	// ExitAuth: not logged in, or the login is no longer valid.
	ExitAuth ExitCode = 6
	// ExitUnreachable: the server or the resource repository cannot be
	// reached. Nothing that is installed gets removed.
	ExitUnreachable ExitCode = 7
	// ExitIncompatible: versions do not fit, such as a command line older
	// than the server requires or a repository schema newer than this one.
	ExitIncompatible ExitCode = 8
	// ExitStateUnknown: the local state is unknown, so the command refuses
	// to act (the manifest cannot be read or parsed, or another process
	// holds the lock).
	ExitStateUnknown ExitCode = 9
)

// Error is a failure the person is told about. Commands return it from their
// Run function; the command line turns it into the exit code and the error
// output, human or JSON.
type Error struct {
	Exit ExitCode
	// Code is a stable English identifier such as "manifest_unreadable".
	// Scripts match on it; it is never translated.
	Code string
	// Message is for people, already in their language.
	Message string
	// Hint is an optional line for people that follows the message, such as
	// how to get help. It is not part of the JSON output.
	Hint string
	// Err is the underlying cause, if any. It is not shown in the JSON
	// output; it is there for logs and for errors.Is.
	Err error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return e.Message + ": " + e.Err.Error()
	}
	return e.Message
}

func (e *Error) Unwrap() error { return e.Err }

// Fail returns an error with the given exit code, stable code and message.
func Fail(exit ExitCode, code, message string) *Error {
	return &Error{Exit: exit, Code: code, Message: message}
}

// Usage returns a usage error (exit code 2).
func Usage(code, message string) *Error {
	return &Error{Exit: ExitUsage, Code: code, Message: message}
}

// Status ends a command that has already written its result but has to exit
// with a code other than 0, such as ExitPending or ExitDiff. It produces no
// output of its own.
type Status ExitCode

func (s Status) Error() string { return "exit status " + strconv.Itoa(int(s)) }

// ExitOf returns the exit code for an error returned by a command: 0 for nil,
// the code of an *Error or a Status, and ExitFailure for anything else.
func ExitOf(err error) ExitCode {
	if err == nil {
		return ExitOK
	}
	var s Status
	if errors.As(err, &s) {
		return ExitCode(s)
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Exit
	}
	return ExitFailure
}
