// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/brand"
	"github.com/code2rich/rigdepot/internal/buildinfo"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
)

func init() { register("", newFeedbackCmd) }

// ErrNotSignedIn is what Env.Connect returns when this computer is not signed in to a server.
var ErrNotSignedIn = errors.New("not signed in to a server")

// feedbackFlags are the options of rgd feedback.
type feedbackFlags struct {
	kind, title, message, resource, severity string
	version                                  int
	ai                                       bool
	tool, toolVersion                        string
	frictions                                []string
}

// The choices of the options, as in the contract (CreateFeedbackRequest, FrictionCount).
var (
	feedbackTypes      = []string{"bug", "suggestion", "question"}
	feedbackSeverities = []string{"low", "medium", "high"}
	frictionTypes      = []string{"tool_error", "waiting_approval", "repeated_correction", "missing_secret", "interrupted", "gate_denied", "sync_failed"}
)

// maxFeedbackBody is how much of standard input is read as the text (the contract's limit is
// 5000 characters; the server says so when it is longer).
const maxFeedbackBody = 64 << 10

// newFeedbackCmd: rgd feedback. It sends feedback to the server this computer is signed in to,
// with the program's version and the operating system, and, from an AI tool, the tool and the
// session's friction counts. Nothing else from the computer goes along: no logs, no files, no
// conversation; the person's own words are the text.
//
// Exit codes: 0 sent; 2 the options or the text are not usable, or the server refused a field;
// 6 not signed in, or the sign-in is no longer valid; 7 the server cannot be reached; 1 any other
// refusal.
func newFeedbackCmd(a *app) *cobra.Command {
	var f feedbackFlags
	cmd := &cobra.Command{
		Use:   "feedback",
		Short: a.p.T(msg.FeedbackShort),
		Args:  a.noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.runFeedback(cmd, f)
		},
	}
	fs := cmd.Flags()
	fs.StringVar(&f.kind, "type", "bug", a.p.T(msg.FlagFeedbackType))
	fs.StringVar(&f.title, "title", "", a.p.T(msg.FlagFeedbackTitle))
	fs.StringVarP(&f.message, "message", "m", "", a.p.T(msg.FlagFeedbackMessage))
	fs.StringVar(&f.resource, "resource", "", a.p.T(msg.FlagFeedbackResource))
	fs.IntVar(&f.version, "resource-version", 0, a.p.T(msg.FlagFeedbackResourceVersion))
	fs.StringVar(&f.severity, "severity", "", a.p.T(msg.FlagFeedbackSeverity))
	fs.BoolVar(&f.ai, "ai", false, a.p.T(msg.FlagFeedbackAi))
	fs.StringVar(&f.tool, "tool", "", a.p.T(msg.FlagFeedbackTool))
	fs.StringVar(&f.toolVersion, "tool-version", "", a.p.T(msg.FlagFeedbackToolVersion))
	fs.StringArrayVar(&f.frictions, "friction", nil, a.p.T(msg.FlagFeedbackFriction))
	return cmd
}

// feedbackDoc is the JSON output of rgd feedback (schema rgd.feedback/v1).
type feedbackDoc struct {
	Number int    `json:"number"`
	Status string `json:"status"`
	Origin string `json:"origin"`
	// Sent are the names of the context fields that went along.
	Sent []string `json:"sent"`
}

func (a *app) runFeedback(cmd *cobra.Command, f feedbackFlags) error {
	path := cmd.CommandPath()
	choice := func(flag, value string, choices []string) error {
		if value == "" || slices.Contains(choices, value) {
			return nil
		}
		return a.usageError("bad_"+strings.TrimPrefix(flag, "--"), a.p.T(msg.FeedbackBadChoice, i18n.V("flag", flag),
			i18n.V("choices", strings.Join(choices, ", ")), i18n.V("value", value)), path)
	}
	if err := choice("--type", f.kind, feedbackTypes); err != nil {
		return err
	}
	if err := choice("--severity", f.severity, feedbackSeverities); err != nil {
		return err
	}
	if strings.TrimSpace(f.title) == "" {
		return a.usageError("no_title", a.p.T(msg.FeedbackNoTitle), path)
	}
	req := apiclient.CreateFeedbackJSONRequestBody{Type: apiclient.CreateFeedbackRequestType(f.kind), Title: f.title}
	body, err := a.feedbackText(f.message)
	if err != nil {
		return err
	}
	if strings.TrimSpace(body) == "" {
		return a.usageError("no_body", a.p.T(msg.FeedbackNoBody), path)
	}
	req.Body = body
	if f.severity != "" {
		s := apiclient.FeedbackSeverity(f.severity)
		req.Severity = &s
	}
	if f.resource != "" {
		req.Resource = &f.resource
	}
	if f.version > 0 {
		req.ResourceVersion = &f.version
	}
	if f.ai {
		req.AiDrafted = &f.ai
	}
	fc, sent, err := a.feedbackContext(f, path)
	if err != nil {
		return err
	}
	req.Context = &fc

	client, err := a.connect(cmd.Context())
	if err != nil {
		return err
	}
	resp, err := client.CreateFeedbackWithResponse(cmd.Context(), req)
	if err != nil {
		e := cliout.Fail(cliout.ExitUnreachable, "unreachable", a.p.T(msg.FeedbackUnreachable, i18n.V("detail", err.Error())))
		e.Err = err
		return e
	}
	if resp.JSON201 == nil {
		return a.feedbackRefused(resp.StatusCode(), resp.ApplicationproblemJSONDefault, path)
	}
	doc := feedbackDoc{Number: resp.JSON201.Number, Status: string(resp.JSON201.Status), Origin: string(resp.JSON201.Origin), Sent: sent}
	return a.out.Result(cliout.Schema("feedback", 1), doc, func(w io.Writer) error {
		lines := []string{a.p.T(msg.FeedbackDone, i18n.V("number", doc.Number))}
		if len(sent) == 0 {
			lines = append(lines, a.p.T(msg.FeedbackSentNothing))
		} else {
			lines = append(lines, a.p.T(msg.FeedbackSentContext, i18n.V("fields", strings.Join(sent, ", "))))
		}
		for _, ln := range lines {
			if _, err := fmt.Fprintln(w, "  "+ln); err != nil {
				return err
			}
		}
		return nil
	})
}

// feedbackText is the text: the option's, or standard input when the option is "-" or not
// given and standard input is not a terminal (nobody would know they are expected to type).
func (a *app) feedbackText(message string) (string, error) {
	if message != "" && message != "-" {
		return message, nil
	}
	if a.env.Stdin == nil || (message == "" && a.env.IsTerminal(a.env.Stdin)) {
		return "", nil
	}
	data, err := io.ReadAll(io.LimitReader(a.env.Stdin, maxFeedbackBody))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// feedbackContext is the context sent along, and the names of its fields: this program's
// version and the operating system always; from an AI tool, the tool, its version and the
// session's friction counts.
//
// TODO(M1-27): the result of the last sync (last_sync) once sync keeps it.
func (a *app) feedbackContext(f feedbackFlags, path string) (apiclient.FeedbackContext, []string, error) {
	version, _, _ := strings.Cut(buildinfo.Get().Version, "+")
	goos := a.env.GOOS
	c := apiclient.FeedbackContext{CliVersion: &version, Os: &goos}
	sent := []string{"cli_version", "os"}
	if f.tool != "" {
		c.Tool = &f.tool
		sent = append(sent, "tool")
	}
	if f.toolVersion != "" {
		c.ToolVersion = &f.toolVersion
		sent = append(sent, "tool_version")
	}
	var frictions []apiclient.FrictionCount
	for _, v := range f.frictions {
		kind, count, ok := strings.Cut(v, "=")
		n, err := strconv.Atoi(count)
		if !ok || err != nil || n < 1 || !slices.Contains(frictionTypes, kind) {
			return c, nil, a.usageError("bad_friction", a.p.T(msg.FeedbackBadFriction, i18n.V("value", v)), path)
		}
		frictions = append(frictions, apiclient.FrictionCount{Type: apiclient.FrictionCountType(kind), Count: n})
	}
	if len(frictions) > 0 {
		c.Frictions = &frictions
		sent = append(sent, "frictions")
	}
	return c, sent, nil
}

// connect returns a client for the server this computer is signed in to, or the error for not
// being signed in (exit code 6).
func (a *app) connect(ctx context.Context) (apiclient.ClientWithResponsesInterface, error) {
	notSignedIn := func(err error) error {
		e := cliout.Fail(cliout.ExitAuth, "not_signed_in", a.p.T(msg.FeedbackNotSignedIn, i18n.V("command", brand.CLI+" login")))
		e.Err = err
		return e
	}
	if a.env.Connect == nil {
		return nil, notSignedIn(ErrNotSignedIn)
	}
	c, err := a.env.Connect(ctx)
	if errors.Is(err, ErrNotSignedIn) {
		return nil, notSignedIn(err)
	}
	if err != nil {
		return nil, err
	}
	return c, nil
}

// feedbackRefused turns an answer other than 201 into the error for the person.
func (a *app) feedbackRefused(status int, p *apiclient.Problem, path string) error {
	detail := http.StatusText(status)
	if p != nil {
		if p.Detail != nil && *p.Detail != "" {
			detail = *p.Detail
		}
		if p.Errors != nil {
			var fields []string
			for _, fe := range *p.Errors {
				fields = append(fields, fe.Field+" ("+fe.Code+")")
			}
			detail += " " + strings.Join(fields, ", ")
		}
	}
	switch {
	case status == http.StatusUnauthorized:
		return cliout.Fail(cliout.ExitAuth, "not_signed_in", a.p.T(msg.FeedbackNotSignedIn, i18n.V("command", brand.CLI+" login")))
	case status == http.StatusBadRequest:
		e := a.usageError("rejected", a.p.T(msg.FeedbackRejected, i18n.V("detail", detail)), path)
		return e
	case status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout:
		return cliout.Fail(cliout.ExitUnreachable, "unreachable", a.p.T(msg.FeedbackUnreachable, i18n.V("detail", detail)))
	}
	return cliout.Fail(cliout.ExitFailure, "refused", a.p.T(msg.FeedbackFailed, i18n.V("status", status), i18n.V("detail", detail)))
}
