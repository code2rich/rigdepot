// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/spf13/cobra"

	"github.com/code2rich/rigdepot/ext/resource"
	"github.com/code2rich/rigdepot/internal/apiclient"
	"github.com/code2rich/rigdepot/internal/cliout"
	"github.com/code2rich/rigdepot/internal/desired"
	"github.com/code2rich/rigdepot/internal/device"
	"github.com/code2rich/rigdepot/internal/i18n"
	"github.com/code2rich/rigdepot/internal/i18n/msg"
	"github.com/code2rich/rigdepot/internal/localconfig"
)

func init() { register("", newEnableCmd) }

// newEnableCmd: rgd enable <resource>. It turns on an optional resource, or turns a recommended
// one the person had turned off back on. Signed in to a server, the choice is the person's on the
// server, for all their computers (the same as the switch in the catalog); otherwise it is kept
// in the local selection (state/selection.json). The next sync installs it. Without a server
// this is the only way to say "I want this one".
func newEnableCmd(a *app) *cobra.Command {
	var src sourceFlags
	cmd := &cobra.Command{
		Use:   "enable <resource>",
		Short: a.p.T(msg.EnableShort),
		Args:  a.exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.changeSelection(cmd.Context(), cmd, src, args[0], true)
		},
	}
	a.addSourceFlags(cmd, &src)
	return cmd
}

// selectionDoc is the JSON output of rgd enable and rgd disable (schema rgd.selection/v1).
type selectionDoc struct {
	Resource string `json:"resource"`
	Kind     string `json:"kind"`
	Policy   string `json:"policy"`
	// Action is "enable" or "disable"; Changed says whether the selection changed.
	Action  string `json:"action"`
	Changed bool   `json:"changed"`
	// Delivered says whether a sync now installs the resource; Reason says why not.
	Delivered bool              `json:"delivered"`
	Reason    string            `json:"reason,omitempty"`
	Selection desired.Selection `json:"selection"`
	// Server is the server the choice is kept on, when signed in; Selection is then empty.
	Server string `json:"server,omitempty"`
}

// changeSelection is rgd enable and rgd disable. The resource's policy comes from the repository:
// what may be turned off is the repository's to say, not this computer's.
func (a *app) changeSelection(ctx context.Context, cmd *cobra.Command, src sourceFlags, code string, enable bool) error {
	l, err := a.local()
	if err != nil {
		return err
	}
	if server := l.cfg.String(localconfig.KeyServer); src.repo == "" && server != "" {
		creds, client, err := a.signedIn(l.cfg, server)
		switch {
		case err == nil:
			return a.changePreference(ctx, creds, client, src, code, enable)
		case !errors.Is(err, errNotSignedIn):
			return err
		case l.cfg.String(localconfig.KeyRepoURL) == "":
			return a.notSignedInError(server)
		}
	}
	st, err := a.desiredState(ctx, l, src)
	if errors.Is(err, errNoSource) {
		return a.noSourceError(cmd.CommandPath())
	}
	if err != nil {
		return err
	}
	ref, meta, err := a.lookupResource(st, code)
	if err != nil {
		return err
	}
	sel, err := desired.ReadSelection(l.data)
	if err != nil {
		return a.stateError(err)
	}
	doc := selectionDoc{Resource: code, Kind: ref.Kind, Policy: string(meta.Policy), Action: "disable"}
	if doc.Policy == "" {
		doc.Policy = string(resource.PolicyRecommended)
	}
	if enable {
		doc.Action = "enable"
		doc.Changed = sel.Enable(code, meta.Policy)
	} else {
		doc.Changed, err = sel.Disable(code, meta.Policy)
		if errors.Is(err, desired.ErrRequired) {
			return cliout.Fail(cliout.ExitFailure, "required", a.p.T(msg.DisableRequired, i18n.V("resource", code)))
		}
		if err != nil {
			return err
		}
	}
	if doc.Changed {
		if err := desired.WriteSelection(l.data, sel); err != nil {
			e := cliout.Fail(cliout.ExitFailure, "selection_write", a.p.T(msg.SelectionWriteFailed, i18n.V("detail", err.Error())))
			e.Err = err
			return e
		}
	}
	_, products, _ := l.source(src)
	delivered, why := desired.Filter{Products: products, Selection: sel}.Decide(code, meta)
	doc.Delivered, doc.Reason, doc.Selection = delivered, string(why), sel
	if doc.Selection.Enabled == nil {
		doc.Selection.Enabled = []string{}
	}
	if doc.Selection.Disabled == nil {
		doc.Selection.Disabled = []string{}
	}
	return a.out.Result(cliout.Schema("selection", 1), doc, func(w io.Writer) error {
		return a.writeSelection(w, doc, src)
	})
}

// changePreference is rgd enable and rgd disable when signed in: the person's choice on the
// server (setCatalogPreference), for all their computers from their next sync. What may be
// turned off is the server's to say.
func (a *app) changePreference(ctx context.Context, creds device.Credentials, client *apiclient.ClientWithResponses, src sourceFlags, code string, enable bool) error {
	cur, err := client.GetCatalogResourceWithResponse(ctx, code)
	if err == nil {
		err = apiclient.Check(cur.HTTPResponse, cur.Body)
	}
	var ae *apiclient.Error
	if errors.As(err, &ae) && ae.Status == http.StatusNotFound {
		return cliout.Fail(cliout.ExitUsage, "no_such_resource", a.p.T(msg.SelectionNoSuchResource, i18n.V("resource", code), i18n.V("url", creds.Server)))
	}
	if err != nil {
		return a.preferenceError(creds.Server, err)
	}
	r := cur.JSON200
	if r == nil {
		return a.preferenceError(creds.Server, errors.New("the answer cannot be read"))
	}
	doc := selectionDoc{Resource: code, Kind: r.Kind, Policy: string(r.Policy), Action: "disable", Server: creds.Server,
		Selection: desired.Selection{Enabled: []string{}, Disabled: []string{}}}
	if enable {
		doc.Action = "enable"
	}
	if !enable && r.Policy == apiclient.Required {
		return cliout.Fail(cliout.ExitFailure, "required", a.p.T(msg.DisableRequiredServer, i18n.V("resource", code)))
	}
	doc.Delivered = r.Enabled
	if r.Enabled != enable {
		resp, err := client.SetCatalogPreferenceWithResponse(ctx, code, apiclient.PreferenceRequest{Enabled: enable})
		if err == nil {
			err = apiclient.Check(resp.HTTPResponse, resp.Body)
		}
		if errors.As(err, &ae) && ae.Code() == "resource.required" {
			return cliout.Fail(cliout.ExitFailure, "required", a.p.T(msg.DisableRequiredServer, i18n.V("resource", code)))
		}
		if err != nil {
			return a.preferenceError(creds.Server, err)
		}
		doc.Changed = true
		if resp.JSON200 != nil {
			doc.Delivered = resp.JSON200.Enabled
		}
	}
	return a.out.Result(cliout.Schema("selection", 1), doc, func(w io.Writer) error {
		res := i18n.V("resource", code)
		var lines []string
		switch {
		case doc.Action == "enable" && doc.Policy == string(resource.PolicyRequired):
			lines = append(lines, a.p.T(msg.EnableRequired, res))
		case doc.Action == "enable" && doc.Changed:
			lines = append(lines, a.p.T(msg.EnableDoneServer, res))
		case doc.Action == "enable":
			lines = append(lines, a.p.T(msg.EnableAlreadyServer, res))
		case doc.Changed:
			lines = append(lines, a.p.T(msg.DisableDoneServer, res))
		default:
			lines = append(lines, a.p.T(msg.DisableAlreadyServer, res))
		}
		sync := i18n.V("command", commandLine("sync", src))
		if doc.Action == "enable" {
			lines = append(lines, a.p.T(msg.EnableSyncHint, sync))
		} else {
			lines = append(lines, a.p.T(msg.DisableSyncHint, sync))
		}
		for _, ln := range lines {
			if _, err := fmt.Fprintln(w, "  "+ln); err != nil {
				return err
			}
		}
		return nil
	})
}

// preferenceError explains a failure to read or change a choice on the server. Nothing was
// changed.
func (a *app) preferenceError(server string, err error) error {
	var ae *apiclient.Error
	if errors.As(err, &ae) && ae.Status == http.StatusUnauthorized {
		return a.remoteError(server, fmt.Errorf("%w: %w", desired.ErrSignedOut, err))
	}
	return a.serverError(server, err)
}

// lookupResource finds a resource in the repository, delivered or not.
func (a *app) lookupResource(st *desired.State, code string) (resource.Ref, resource.Meta, error) {
	if r, ok := st.Lookup(code); ok {
		return r.Ref, r.Meta, nil
	}
	for _, x := range st.Excluded {
		if x.Ref.Code == code {
			return x.Ref, x.Meta, nil
		}
	}
	for _, f := range st.Failed {
		if f.Ref.Code == code {
			// Its policy is unknown: whether it may be turned off cannot be told.
			return resource.Ref{}, resource.Meta{}, cliout.Fail(cliout.ExitFailure, "resource_unreadable",
				a.p.T(msg.SelectionUnreadable, i18n.V("resource", code), i18n.V("path", f.Path)))
		}
	}
	return resource.Ref{}, resource.Meta{}, cliout.Fail(cliout.ExitUsage, "no_such_resource",
		a.p.T(msg.SelectionNoSuchResource, i18n.V("resource", code), i18n.V("url", st.Source.URL)))
}

// writeSelection is the human output of enable and disable: what changed and what the next sync
// does about it.
func (a *app) writeSelection(w io.Writer, doc selectionDoc, src sourceFlags) error {
	res := i18n.V("resource", doc.Resource)
	var lines []string
	switch {
	case doc.Action == "enable" && doc.Policy == string(resource.PolicyRequired):
		lines = append(lines, a.p.T(msg.EnableRequired, res))
	case doc.Action == "enable" && doc.Changed:
		lines = append(lines, a.p.T(msg.EnableDone, res))
	case doc.Action == "enable":
		lines = append(lines, a.p.T(msg.EnableAlready, res))
	case doc.Changed:
		lines = append(lines, a.p.T(msg.DisableDone, res))
	default:
		lines = append(lines, a.p.T(msg.DisableAlready, res))
	}
	sync := i18n.V("command", commandLine("sync", src))
	switch {
	case doc.Action == "enable" && !doc.Delivered:
		lines = append(lines, a.p.T(msg.EnableNotDelivered, res, i18n.V("reason", a.p.T(excludedReason(desired.Reason(doc.Reason))))))
	case doc.Action == "enable":
		lines = append(lines, a.p.T(msg.EnableSyncHint, sync))
	default:
		lines = append(lines, a.p.T(msg.DisableSyncHint, sync))
	}
	for _, ln := range lines {
		if _, err := fmt.Fprintln(w, "  "+ln); err != nil {
			return err
		}
	}
	return nil
}

// excludedReason is the message key that says why a resource is not delivered.
func excludedReason(r desired.Reason) string {
	switch r {
	case desired.ExcludedPersonal:
		return msg.SelectionReasonPersonal
	case desired.ExcludedProduct:
		return msg.SelectionReasonProduct
	case desired.ExcludedAudience:
		return msg.SelectionReasonAudience
	case desired.ExcludedDisabled:
		return msg.SelectionReasonDisabled
	}
	return msg.SelectionReasonNotEnabled
}
