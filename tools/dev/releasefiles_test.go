// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"slices"
	"testing"
)

func TestClassifyLicense(t *testing.T) {
	const (
		mit    = "MIT License\n\nPermission is hereby granted, free of charge, to any person obtaining a copy"
		apache = "                                 Apache License\n                           Version 2.0, January 2004"
		bsd    = "Redistribution and use in source and binary forms, with or without\nmodification, are permitted"
		isc    = "Permission to use, copy, modify, and/or distribute this software for any\npurpose with or without fee is hereby granted"
		gpl    = "                    GNU GENERAL PUBLIC LICENSE\n                       Version 3, 29 June 2007"
		agpl   = "GNU AFFERO GENERAL PUBLIC LICENSE"
	)
	tests := []struct {
		name               string
		text               string
		allowed, forbidden []string
	}{
		{"MIT", mit, []string{"MIT"}, nil},
		{"Apache", apache, []string{"Apache-2.0"}, nil},
		{"BSD across a line break", bsd, []string{"BSD"}, nil},
		{"ISC", isc, []string{"ISC"}, nil},
		{"GPL", gpl, nil, []string{"GPL"}},
		{"AGPL", agpl, nil, []string{"AGPL"}},
		{"two licenses in one file, one forbidden", mit + "\n\n" + gpl, []string{"MIT"}, []string{"GPL"}},
		{"Apache without its version is not recognised", "Apache License", nil, nil},
		{"public domain is not recognised", "The author disclaims copyright to this source code.", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allowed, forbidden := classifyLicense(tt.text)
			if !slices.Equal(allowed, tt.allowed) || !slices.Equal(forbidden, tt.forbidden) {
				t.Errorf("classifyLicense = %v, %v; want %v, %v", allowed, forbidden, tt.allowed, tt.forbidden)
			}
		})
	}
}

func TestLicenseFileNames(t *testing.T) {
	for name, want := range map[string]bool{
		"LICENSE": true, "LICENSE.md": true, "LICENCE.txt": true, "COPYING": true, "NOTICE": true,
		"PATENTS": true, "LICENSE-3RD-PARTY.md": true, "license-sqlite": true,
		"README.md": false, "licenses.go": false, "doc.go": false,
	} {
		if got := licenseFileName.MatchString(name); got != want {
			t.Errorf("%s: license file %v, want %v", name, got, want)
		}
	}
	for name, want := range map[string]bool{
		"NOTICE": true, "PATENTS": true, "LICENSE-3RD-PARTY.md": true, "THIRD_PARTY_NOTICES": true,
		"LICENSE": false, "LICENSE-SQLITE": false,
	} {
		if got := isNoticeFile(name); got != want {
			t.Errorf("%s: notice %v, want %v", name, got, want)
		}
	}
}
