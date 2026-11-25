// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

//go:build windows

package keyring

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

// protect encrypts value for the current Windows user with DPAPI. It needs the user's profile to
// be loaded, not a desktop session, so it works where the Credential Manager does not.
func protect(value []byte) ([]byte, error) {
	return dpapi(value, func(in, out *windows.DataBlob) error {
		return windows.CryptProtectData(in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, out)
	})
}

func unprotect(data []byte) ([]byte, error) {
	return dpapi(data, func(in, out *windows.DataBlob) error {
		return windows.CryptUnprotectData(in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, out)
	})
}

func dpapi(data []byte, call func(in, out *windows.DataBlob) error) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(data))}
	if len(data) > 0 {
		in.Data = &data[0]
	}
	var out windows.DataBlob
	if err := call(&in, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...), nil
}
