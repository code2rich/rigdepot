// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package authnapi

import (
	"context"
	"net/url"
	"time"

	"github.com/code2rich/rigdepot/internal/server/authn"
	"github.com/code2rich/rigdepot/internal/server/httpapi"
)

// The operations of the device code flow and of my devices. Their rules
// are package authn's (authn.Devices); here they are only translated.

// DevicePagePath is the console's confirmation page, below the public
// address: verification_uri.
const DevicePagePath = "/device"

// errNoDevices answers the device operations of a server assembled without
// authn.Devices.
func errNoDevices() error { return httpapi.ErrNotImplemented }

func seconds(d time.Duration) int { return int((d + time.Second - 1) / time.Second) }

// RequestDeviceCode implements httpapi.StrictServerInterface: the first step
// of rgd login. It is limited per client address (Middleware).
func (h *Ops) RequestDeviceCode(ctx context.Context, req httpapi.RequestDeviceCodeRequestObject) (httpapi.RequestDeviceCodeResponseObject, error) {
	if h.devices == nil {
		return nil, errNoDevices()
	}
	if req.Body == nil {
		return nil, errNoBody(ctx)
	}
	b := req.Body
	code, err := h.devices.StartDeviceLogin(ctx, authn.DeviceInfo{
		Name: b.Name, OS: string(b.Os), Arch: b.Arch, CLIVersion: b.CliVersion, PublicKey: b.PublicKey, KeyStorage: string(b.KeyStorage),
	})
	if err != nil {
		return nil, fail(ctx, err)
	}
	userCode := authn.ShowUserCode(code.UserCode)
	page := h.base + DevicePagePath
	return httpapi.RequestDeviceCode200JSONResponse{
		DeviceCode: code.DeviceCode, UserCode: userCode,
		VerificationUri: page, VerificationUriComplete: page + "?code=" + url.QueryEscape(userCode),
		ExpiresIn: seconds(code.ExpiresIn), Interval: seconds(code.Interval),
	}, nil
}

// PollDeviceToken implements httpapi.StrictServerInterface. Its answers
// before the approval are 400 problem documents (RFC 8628, 3.5).
func (h *Ops) PollDeviceToken(ctx context.Context, req httpapi.PollDeviceTokenRequestObject) (httpapi.PollDeviceTokenResponseObject, error) {
	if h.devices == nil {
		return nil, errNoDevices()
	}
	if req.Body == nil {
		return nil, errNoBody(ctx)
	}
	issued, err := h.devices.PollDevice(ctx, req.Body.DeviceCode)
	if err != nil {
		return nil, fail(ctx, err)
	}
	return httpapi.PollDeviceToken200JSONResponse{
		DeviceToken: issued.Token, DeviceId: issued.DeviceID,
		User: httpapi.UserRef{Id: issued.User.ID, Username: issued.User.Username, DisplayName: issued.User.DisplayName},
	}, nil
}

// GetDeviceRequest implements httpapi.StrictServerInterface.
func (h *Ops) GetDeviceRequest(ctx context.Context, req httpapi.GetDeviceRequestRequestObject) (httpapi.GetDeviceRequestResponseObject, error) {
	if h.devices == nil {
		return nil, errNoDevices()
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	r, err := h.devices.DeviceRequest(ctx, c, req.UserCode)
	if err != nil {
		return nil, fail(ctx, err)
	}
	return httpapi.GetDeviceRequest200JSONResponse{
		UserCode: r.UserCode, Name: r.Name, Os: r.OS, Arch: r.Arch, CliVersion: r.CLIVersion,
		Status: httpapi.DeviceRequestStatus(r.Status), RequestedAt: r.RequestedAt, ExpiresAt: r.ExpiresAt,
	}, nil
}

// ApproveDeviceRequest implements httpapi.StrictServerInterface.
func (h *Ops) ApproveDeviceRequest(ctx context.Context, req httpapi.ApproveDeviceRequestRequestObject) (httpapi.ApproveDeviceRequestResponseObject, error) {
	if h.devices == nil {
		return nil, errNoDevices()
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.devices.ApproveDevice(ctx, c, req.UserCode); err != nil {
		return nil, fail(ctx, err)
	}
	return httpapi.ApproveDeviceRequest204Response{}, nil
}

// DenyDeviceRequest implements httpapi.StrictServerInterface.
func (h *Ops) DenyDeviceRequest(ctx context.Context, req httpapi.DenyDeviceRequestRequestObject) (httpapi.DenyDeviceRequestResponseObject, error) {
	if h.devices == nil {
		return nil, errNoDevices()
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.devices.DenyDevice(ctx, c, req.UserCode); err != nil {
		return nil, fail(ctx, err)
	}
	return httpapi.DenyDeviceRequest204Response{}, nil
}

// DeviceSync is what a computer said at its last sync: when it finished,
// how it went and the AI tools it found (package delivery keeps it).
type DeviceSync struct {
	LastSync httpapi.LastSync
	Tools    []httpapi.DetectedTool
}

// ListMyDevices implements httpapi.StrictServerInterface. What the command
// line last reported (the last sync, the tools) comes from the install
// reports (Options.DeviceSyncs); a computer that never synced has none.
func (h *Ops) ListMyDevices(ctx context.Context, _ httpapi.ListMyDevicesRequestObject) (httpapi.ListMyDevicesResponseObject, error) {
	if h.devices == nil {
		return nil, errNoDevices()
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	ds, err := h.devices.MyDevices(ctx, c)
	if err != nil {
		return nil, fail(ctx, err)
	}
	syncs := map[string]DeviceSync{}
	if h.syncs != nil && len(ds) > 0 {
		ids := make([]string, len(ds))
		for i, d := range ds {
			ids[i] = d.ID
		}
		if syncs, err = h.syncs(ctx, ids); err != nil {
			return nil, err
		}
	}
	out := httpapi.ListMyDevices200JSONResponse{Items: make([]httpapi.Device, len(ds))}
	for i, d := range ds {
		out.Items[i] = httpapi.Device{
			Id: d.ID, Name: d.Name, Os: httpapi.DeviceOs(d.OS), Arch: d.Arch, CliVersion: d.CLIVersion,
			KeyStorage: httpapi.DeviceKeyStorage(d.KeyStorage), Current: d.Current,
			CreatedAt: d.CreatedAt, LastSeenAt: d.LastSeenAt, Tools: []httpapi.DetectedTool{},
		}
		if sy, ok := syncs[d.ID]; ok {
			last := sy.LastSync
			out.Items[i].LastSync = &last
			if sy.Tools != nil {
				out.Items[i].Tools = sy.Tools
			}
		}
	}
	return out, nil
}

// SignOutDevice implements httpapi.StrictServerInterface.
func (h *Ops) SignOutDevice(ctx context.Context, req httpapi.SignOutDeviceRequestObject) (httpapi.SignOutDeviceResponseObject, error) {
	if h.devices == nil {
		return nil, errNoDevices()
	}
	c, err := caller(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.devices.SignOutDevice(ctx, c, req.DeviceId); err != nil {
		return nil, fail(ctx, err)
	}
	return httpapi.SignOutDevice204Response{}, nil
}
