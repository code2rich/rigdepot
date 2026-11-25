// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

// Package structure is the check rule for the shape of a resource: the files it must have, the
// fields they must contain, and the limits on its size and paths. Must fix.
//
// For a skill: SKILL.md exists and starts with front matter that has a name equal to the
// resource's code and a description. For an MCP server: <code>.yaml is a YAML mapping with
// meta.name and a server entry of a known type, and ${secret:…} appears only in env and headers,
// because the URL, the command and its arguments end up in process lists and logs. For both: the
// code is well formed, at most 200 files and 5 MB, no path longer than 180 bytes or deeper than
// 8 levels, and no two names in one directory that differ only in case.
//
// What a JSON Schema can say about the shape of a file is checked when the repository is read;
// this rule checks what a schema cannot, and points at the line.
//
// Message keys, all under check.structure.:
//
//	code_invalid            the code is not 2 to 48 lower-case letters, digits and hyphens      {code}
//	too_many_files          more files than the limit                                          {count, limit}
//	too_large               more bytes than the limit                                          {bytes, limit}
//	path_too_long           a path longer than the limit                                       {bytes, limit}
//	path_too_deep           a path with more levels than the limit                             {depth, limit}
//	case_clash              two names in one directory that differ only in case                {other}
//	skill_md_missing        a skill without SKILL.md
//	front_matter_missing    SKILL.md does not start with a line "---"
//	front_matter_unterminated  the front matter has no closing "---"
//	front_matter_invalid    the front matter is not a YAML mapping
//	name_missing            no name in the front matter
//	name_mismatch           the name is not the code                                           {name, code}
//	description_missing     no description in the front matter
//	file_missing            a single-file resource without its file                            {file}
//	yaml_invalid            the file is not a YAML mapping
//	meta_missing            no meta mapping
//	meta_name_missing       no meta.name
//	server_missing          no server mapping
//	server_type_invalid     server.type is not stdio, http or sse                              {type}
//	server_command_missing  a stdio server without command
//	server_url_missing      an http or sse server without url
//	secret_not_allowed      ${secret:…} outside env and headers                               {field}
//	delivery_invalid        delivery is not native or on-demand                                {delivery}
package structure
