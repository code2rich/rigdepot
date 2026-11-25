// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"path"
	"regexp"
	"slices"
	"strings"
)

// The message keys FromProfile puts into Detection.Reason.
const (
	// ReasonUnreadable: a file or directory the detection had to look at exists but could not
	// be read.
	ReasonUnreadable = "adapter.detect.unreadable"
	// ReasonLookPath: looking for the tool's executable failed for another reason than its
	// absence.
	ReasonLookPath = "adapter.detect.lookpath_failed"
)

// FromProfile returns an adapter that does everything its profile can say, so that a tool shaped
// like the others needs no code at all. What a profile cannot express is written by embedding
// the result and overriding single methods:
//
//	type opencode struct{ adapter.Adapter }
//
//	func New() adapter.Adapter {
//		return &opencode{Adapter: adapter.FromProfile(profileyaml.MustParse(profileYAML))}
//	}
//
//	func (o *opencode) Plan(ctx context.Context, req adapter.PlanRequest) (adapter.Plan, error) { … }
//
// The adapter it returns behaves as follows.
//
// Detect resolves every root of the profile (see Root), then counts the tool as installed when
// one of Detect.Bins is found by Env.LookPath or one of Detect.Dirs is a directory. Anything that
// exists but cannot be read makes the status Unknown. When the tool is installed and the profile
// has a version probe, the version is the probe's first group, or "" when the probe fails.
//
// Capabilities is the zero value for a profile without a verification record. Otherwise it
// reports Skills, Commands and Agents for the targets the profile has, and MCP over stdio and
// HTTP when it has an MCP target. It never reports MCPCaps.EnvRef: how a tool spells "take this
// from an environment variable" is the adapter's own code.
//
// Plan translates each artifact independently:
//
//   - SkillDir becomes a WriteTree of the skills directory plus the artifact's name, with the
//     same files and local directories.
//   - Command and SubAgent become a WriteFile in the commands or agents directory, named after
//     the artifact plus the profile's extension, holding a Markdown file whose YAML front matter
//     carries the description (and, for a sub-agent, its name).
//   - MCPServer becomes a SetKey of the profile's key path plus the entry name, with the value
//     {"type":"stdio","command":…,"args":[…],"env":{…}} or {"type":"http","url":…,"headers":{…}},
//     the shape the common .mcp.json format uses. A server that refers to a personal secret is
//     reported as NeedsSecretSupport. Before that, Plan reads the configuration file through Env:
//     when it cannot be read or does not parse, every server is reported as ConfigUnreadable;
//     when more than one of MCPTarget.Files exists, or the key path is taken by something that
//     is not an object, every server is reported as ConfigUnwritable. JSON and JSONC files are
//     read; a profile adapter cannot parse TOML, so for a TOML file that already exists every
//     server is reported as ConfigUnwritable, and an adapter for such a tool plans its MCP
//     servers itself.
//   - An artifact the capabilities do not cover is reported as Unsupported.
//
// An artifact whose name is not a single path element makes Plan fail: that is a defect in the
// resource kind that produced it, not a property of the person's computer.
//
// The returned adapter shares p; the caller must not change p afterwards.
func FromProfile(p Profile) Adapter {
	return &profileAdapter{p: p}
}

type profileAdapter struct {
	p Profile
}

func (a *profileAdapter) ID() string       { return a.p.ID }
func (a *profileAdapter) Profile() Profile { return a.p }

func (a *profileAdapter) Detect(ctx context.Context, env Env) (Detection, error) {
	if err := ctx.Err(); err != nil {
		return Detection{}, err
	}
	roots, err := ResolveRoots(a.p.Roots, env)
	if err != nil {
		return Detection{}, fmt.Errorf("adapter %s: %w", a.p.ID, err)
	}
	d := Detection{Status: NotInstalled, Roots: roots}

	bin := ""
	for _, name := range a.p.Detect.Bins {
		if env.LookPath == nil {
			break
		}
		_, err := env.LookPath(name)
		if err == nil {
			bin = name
			break
		}
		if !errors.Is(err, exec.ErrNotFound) && !errors.Is(err, exec.ErrDot) && !errors.Is(err, fs.ErrNotExist) {
			return Detection{Status: Unknown, Roots: roots, Reason: ReasonLookPath}, nil
		}
	}
	found := bin != ""
	for _, tmpl := range a.p.Detect.Dirs {
		dir, err := ExpandPath(tmpl, roots)
		if err != nil {
			return Detection{}, fmt.Errorf("adapter %s: %w", a.p.ID, err)
		}
		if env.Stat == nil {
			return Detection{Status: Unknown, Roots: roots, Reason: ReasonUnreadable}, nil
		}
		info, err := env.Stat(dir)
		switch {
		case err == nil:
			found = found || info.IsDir()
		case errors.Is(err, fs.ErrNotExist):
		default:
			return Detection{Status: Unknown, Roots: roots, Reason: ReasonUnreadable}, nil
		}
	}
	if !found {
		return d, nil
	}
	d.Status = Installed

	probe := a.p.Detect.Version
	if probe == nil || len(probe.Run) == 0 || env.Version == nil {
		return d, nil
	}
	re, err := regexp.Compile(probe.Match)
	if err != nil || re.NumSubexp() < 1 {
		return Detection{}, fmt.Errorf("adapter %s: the version probe's match %q is not a regular expression with a group", a.p.ID, probe.Match)
	}
	out, err := env.Version(ctx, probe.Run[0], probe.Run[1:]...)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Detection{}, ctxErr
		}
		return d, nil // the version stays unknown; the tool is still there
	}
	if m := re.FindStringSubmatch(out); m != nil {
		d.Version = m[1]
	}
	return d, nil
}

func (a *profileAdapter) Capabilities(Detection) Capabilities {
	if a.p.Verified == nil {
		return Capabilities{}
	}
	return Capabilities{
		Skills:   a.p.Skills != nil,
		Commands: a.p.Commands != nil,
		Agents:   a.p.Agents != nil,
		MCP:      MCPCaps{Stdio: a.p.MCP != nil, HTTP: a.p.MCP != nil},
	}
}

func (a *profileAdapter) Plan(ctx context.Context, req PlanRequest) (Plan, error) {
	if err := ctx.Err(); err != nil {
		return Plan{}, err
	}
	if req.Detection.Status != Installed {
		return Plan{}, fmt.Errorf("adapter %s: cannot plan for a tool whose status is %s", a.p.ID, req.Detection.Status)
	}
	caps := a.Capabilities(req.Detection)
	roots := req.Detection.Roots
	var plan Plan
	skip := func(art Artifact, reason SkipReason, detail string) {
		plan.Skips = append(plan.Skips, Skip{Resource: art.Ref(), Reason: reason, Detail: detail})
	}
	var mcp *mcpConfig // read once, on the first MCP server

	for _, art := range req.Artifacts {
		switch x := art.(type) {
		case SkillDir:
			if !caps.Skills {
				skip(art, Unsupported, "the tool has no place for skills")
				continue
			}
			dir, err := a.child(a.p.Skills.Dir, x.DirName, "", roots)
			if err != nil {
				return Plan{}, err
			}
			files := make([]File, len(x.Files))
			for i, f := range x.Files {
				files[i] = File{Path: f.Path, Data: bytes.Clone(f.Data), Exec: f.Exec}
			}
			plan.Ops = append(plan.Ops, WriteTree{Resource: x.Resource, Dir: dir, Files: files, Local: slices.Clone(x.Local)})

		case Command:
			if !caps.Commands {
				skip(art, Unsupported, "the tool has no place for slash commands")
				continue
			}
			file, err := a.child(a.p.Commands.Dir, x.CmdName, a.p.Commands.Ext, roots)
			if err != nil {
				return Plan{}, err
			}
			data := markdown([][2]string{{"description", x.Description}}, x.Body)
			plan.Ops = append(plan.Ops, WriteFile{Resource: x.Resource, Path: file, Data: data})

		case SubAgent:
			if !caps.Agents {
				skip(art, Unsupported, "the tool has no place for sub-agents")
				continue
			}
			file, err := a.child(a.p.Agents.Dir, x.AgentName, a.p.Agents.Ext, roots)
			if err != nil {
				return Plan{}, err
			}
			data := markdown([][2]string{{"name", x.AgentName}, {"description", x.Description}}, x.Body)
			plan.Ops = append(plan.Ops, WriteFile{Resource: x.Resource, Path: file, Data: data})

		case MCPServer:
			if (x.Transport == Stdio && !caps.MCP.Stdio) || (x.Transport == HTTP && !caps.MCP.HTTP) {
				skip(art, Unsupported, "the tool does not take MCP servers over "+string(x.Transport))
				continue
			}
			if err := checkName(x.EntryName); err != nil {
				return Plan{}, fmt.Errorf("adapter %s: MCP server %s: %w", a.p.ID, x.Resource, err)
			}
			if mcp == nil {
				var err error
				if mcp, err = a.readMCPConfig(req.Env, roots); err != nil {
					return Plan{}, err
				}
			}
			if mcp.reason != "" {
				skip(art, mcp.reason, mcp.detail)
				continue
			}
			value, secret, err := commonMCPValue(x)
			if err != nil {
				skip(art, Unsupported, err.Error())
				continue
			}
			if secret != "" {
				skip(art, NeedsSecretSupport, "refers to the personal secret "+secret+" and the tool has no way to take it from the environment")
				continue
			}
			plan.Ops = append(plan.Ops, SetKey{
				Resource: x.Resource,
				File:     mcp.file,
				Format:   a.p.MCP.Format,
				Path:     append(slices.Clone(a.p.MCP.Path), x.EntryName),
				Value:    value,
			})

		default:
			skip(art, Unsupported, fmt.Sprintf("unknown artifact %T", art))
		}
	}
	return plan, nil
}

// child returns the path of an entry named name+ext inside the directory template dir.
func (a *profileAdapter) child(dir, name, ext string, roots map[string]string) (string, error) {
	if err := checkName(name); err != nil {
		return "", fmt.Errorf("adapter %s: %w", a.p.ID, err)
	}
	base, err := ExpandPath(dir, roots)
	if err != nil {
		return "", fmt.Errorf("adapter %s: %w", a.p.ID, err)
	}
	return base + "/" + name + ext, nil
}

// checkName refuses a name that is not exactly one path element.
func checkName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:\x00") {
		return fmt.Errorf("the name %q is not a single path element", name)
	}
	return nil
}

// mcpConfig is what Plan found out about the MCP configuration file.
type mcpConfig struct {
	file   string     // the file to write
	reason SkipReason // non-empty when no server can be written
	detail string
}

func (a *profileAdapter) readMCPConfig(env Env, roots map[string]string) (*mcpConfig, error) {
	var existing []string
	var data []byte
	last := ""
	for _, tmpl := range a.p.MCP.Files {
		file, err := ExpandPath(tmpl, roots)
		if err != nil {
			return nil, fmt.Errorf("adapter %s: %w", a.p.ID, err)
		}
		last = file
		if env.ReadFile == nil {
			return &mcpConfig{file: file, reason: ConfigUnreadable, detail: file + ": cannot be read"}, nil
		}
		b, err := env.ReadFile(file)
		switch {
		case err == nil:
			existing = append(existing, file)
			data = b
		case errors.Is(err, fs.ErrNotExist):
		default:
			return &mcpConfig{file: file, reason: ConfigUnreadable, detail: file + ": cannot be read"}, nil
		}
	}
	switch len(existing) {
	case 0:
		return &mcpConfig{file: last}, nil // created by the executor
	case 1:
	default:
		return &mcpConfig{file: existing[0], reason: ConfigUnwritable,
			detail: "both " + strings.Join(existing, " and ") + " exist; it is not known which one the tool reads"}, nil
	}
	c := &mcpConfig{file: existing[0]}
	if a.p.MCP.Format == TOML {
		c.reason, c.detail = ConfigUnwritable, c.file+": this adapter cannot read TOML"
		return c, nil
	}
	doc, offset, err := decodeConfig(data, a.p.MCP.Format)
	if err != nil {
		c.reason, c.detail = ConfigUnreadable, fmt.Sprintf("%s: not valid %s at byte %d", c.file, a.p.MCP.Format, offset)
		return c, nil
	}
	var cur any = doc
	for i := 0; ; i++ {
		obj, ok := cur.(map[string]any)
		if !ok {
			c.reason, c.detail = ConfigUnwritable, fmt.Sprintf("%s: %s is not an object", c.file, keyPath(a.p.MCP.Path[:i]))
			return c, nil
		}
		if i == len(a.p.MCP.Path) {
			return c, nil
		}
		next, ok := obj[a.p.MCP.Path[i]]
		if !ok {
			return c, nil // the executor creates the missing levels
		}
		cur = next
	}
}

func keyPath(p []string) string {
	if len(p) == 0 {
		return "the top level"
	}
	return strings.Join(p, ".")
}

// decodeConfig parses a JSON or JSONC file. On failure it returns the byte offset of the error.
func decodeConfig(data []byte, format Format) (any, int64, error) {
	text := StandardJSON(data, format == JSONC)
	dec := json.NewDecoder(bytes.NewReader(text))
	var doc any
	if err := dec.Decode(&doc); err != nil {
		var syn *json.SyntaxError
		if errors.As(err, &syn) {
			return nil, syn.Offset, err
		}
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return nil, int64(len(data)), err // the file ends in the middle of a value, or is empty
		}
		return nil, dec.InputOffset(), err
	}
	if _, err := dec.Token(); err == nil {
		return nil, dec.InputOffset(), errors.New("more than one value")
	}
	return doc, 0, nil
}

// commonMCPValue builds the value of an MCP entry in the common shape. It returns the name of
// the first secret the server refers to instead of a value when there is one.
func commonMCPValue(s MCPServer) (map[string]any, string, error) {
	values := func(m map[string]Value) (map[string]any, string) {
		out := make(map[string]any, len(m))
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			if m[k].Secret != "" {
				return nil, m[k].Secret
			}
			out[k] = m[k].Literal
		}
		return out, ""
	}
	switch s.Transport {
	case Stdio:
		env, secret := values(s.Env)
		if secret != "" {
			return nil, secret, nil
		}
		args := make([]any, len(s.Args))
		for i, a := range s.Args {
			args[i] = a
		}
		return map[string]any{"type": "stdio", "command": s.Command, "args": args, "env": env}, "", nil
	case HTTP:
		headers, secret := values(s.Headers)
		if secret != "" {
			return nil, secret, nil
		}
		return map[string]any{"type": "http", "url": s.URL, "headers": headers}, "", nil
	}
	return nil, "", fmt.Errorf("unknown transport %q", s.Transport)
}

// markdown returns a Markdown file with YAML front matter. The values are written as
// double-quoted scalars, which YAML reads the way JSON writes them.
func markdown(fields [][2]string, body string) []byte {
	var b bytes.Buffer
	b.WriteString("---\n")
	for _, f := range fields {
		b.WriteString(f[0])
		b.WriteString(": ")
		b.Write(quote(f[1]))
		b.WriteString("\n")
	}
	b.WriteString("---\n\n")
	b.WriteString(body)
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\n")
	}
	return b.Bytes()
}

func quote(s string) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s) // a string always encodes
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// ResolveRoots works out where each of a profile's roots is on the computer Env describes, in
// the form described in the package documentation. A root is the value of its environment
// variable with Join appended when the variable holds an absolute path for env.GOOS, and Default
// otherwise. A Default may start with another root ("{config}/…"); a cycle or a root that does
// not exist is an error in the profile.
func ResolveRoots(roots map[string]Root, env Env) (map[string]string, error) {
	out := make(map[string]string, len(roots))
	visiting := map[string]bool{}
	var resolve func(name string) (string, error)
	resolve = func(name string) (string, error) {
		if v, ok := out[name]; ok {
			return v, nil
		}
		r, ok := roots[name]
		if !ok {
			return "", fmt.Errorf("the profile has no root %q", name)
		}
		if visiting[name] {
			return "", fmt.Errorf("the root %q is defined in terms of itself", name)
		}
		visiting[name] = true
		defer delete(visiting, name)
		if r.Env != "" && env.Getenv != nil {
			if p, ok := envPath(env, env.Getenv(r.Env)); ok {
				if r.Join != "" {
					p = path.Join(p, r.Join)
				}
				out[name] = p
				return p, nil
			}
		}
		v, err := expandWith(r.Default, resolve)
		if err != nil {
			return "", err
		}
		out[name] = v
		return v, nil
	}
	for name := range roots {
		if _, err := resolve(name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ExpandPath replaces a leading "{name}" in a path template by the root of that name. A template
// that starts with "~" is returned as it is.
func ExpandPath(tmpl string, roots map[string]string) (string, error) {
	return expandWith(tmpl, func(name string) (string, error) {
		v, ok := roots[name]
		if !ok {
			return "", fmt.Errorf("the path %q refers to the root %q, which was not resolved", tmpl, name)
		}
		return v, nil
	})
}

func expandWith(tmpl string, root func(string) (string, error)) (string, error) {
	if tmpl == "~" || strings.HasPrefix(tmpl, "~/") {
		return tmpl, nil
	}
	if strings.HasPrefix(tmpl, "{") {
		if end := strings.IndexByte(tmpl, '}'); end > 0 {
			base, err := root(tmpl[1:end])
			if err != nil {
				return "", err
			}
			return base + tmpl[end+1:], nil
		}
	}
	return "", fmt.Errorf("the path %q starts with neither \"~/\" nor a root in braces", tmpl)
}

// envPath turns the value of an environment variable into a path in the package's form: forward
// slashes, and "~/…" inside the home directory. It reports false for an empty or relative value,
// which does not count: it would be relative to wherever the tool was started.
func envPath(env Env, value string) (string, bool) {
	windows := env.GOOS == "windows"
	if !isAbs(value, windows) {
		return "", false
	}
	p := cleanAbs(value, windows)
	if isAbs(env.Home, windows) {
		home := cleanAbs(env.Home, windows)
		same := func(a, b string) bool {
			if windows {
				return strings.EqualFold(a, b)
			}
			return a == b
		}
		if same(p, home) {
			return "~", true
		}
		prefix := strings.TrimSuffix(home, "/") + "/"
		if len(p) > len(prefix) && same(p[:len(prefix)], prefix) {
			return "~/" + p[len(prefix):], true
		}
	}
	return p, true
}

func isAbs(p string, windows bool) bool {
	if !windows {
		return strings.HasPrefix(p, "/")
	}
	if strings.HasPrefix(p, `\\`) || strings.HasPrefix(p, "//") {
		return true
	}
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') &&
		(('a' <= p[0] && p[0] <= 'z') || ('A' <= p[0] && p[0] <= 'Z'))
}

func cleanAbs(p string, windows bool) string {
	if windows {
		p = strings.ReplaceAll(p, `\`, "/")
		if strings.HasPrefix(p, "//") {
			return "/" + path.Clean(p[1:])
		}
	}
	return path.Clean(p)
}

// StandardJSON returns the text of a JSON or JSONC file as strict JSON with every byte at the
// same offset: a byte order mark and, when jsonc is true, comments and trailing commas are
// replaced by spaces (line breaks inside comments are kept). Strict JSON keeps its comments, so
// that a JSON file someone put a comment into does not parse.
func StandardJSON(data []byte, jsonc bool) []byte {
	out := bytes.Clone(data)
	if bytes.HasPrefix(out, []byte("\xef\xbb\xbf")) {
		copy(out, "   ")
	}
	if !jsonc {
		return out
	}
	blank := func(from, to int) {
		for i := from; i < to; i++ {
			if out[i] != '\n' && out[i] != '\r' {
				out[i] = ' '
			}
		}
	}
	lastComma := -1 // a comma that may turn out to be trailing
	for i := 0; i < len(out); i++ {
		switch c := out[i]; {
		case c == '"':
			lastComma = -1
			for i++; i < len(out) && out[i] != '"'; i++ {
				if out[i] == '\\' {
					i++
				}
			}
		case c == '/' && i+1 < len(out) && out[i+1] == '/':
			end := bytes.IndexByte(out[i:], '\n')
			if end < 0 {
				end = len(out) - i
			}
			blank(i, i+end)
			i += end - 1
		case c == '/' && i+1 < len(out) && out[i+1] == '*':
			end := bytes.Index(out[i+2:], []byte("*/"))
			if end < 0 {
				return out // unterminated comment: leave it for the parser to refuse
			}
			blank(i, i+2+end+2)
			i += 2 + end + 1
		case c == ',':
			lastComma = i
		case c == '}' || c == ']':
			if lastComma >= 0 {
				out[lastComma] = ' '
			}
			lastComma = -1
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
		default:
			lastComma = -1
		}
	}
	return out
}
