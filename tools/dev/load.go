// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Rigdepot Authors

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The load test of rgd-server (M1-48, docs/architecture/08-deployment.md, minimum hardware): a
// server in a container limited to 2 CPUs and 2 GB, with SQLite, and this process playing an
// organisation's people, their computers and the console:
//
//  1. onboarding: N people accept an invitation and sign one computer in each (device code);
//  2. publishing: resources uploaded and published one after another, and a resource repository
//     of many resources imported over HTTPS (git's http-backend on this machine);
//  3. first sync: every computer fetches its desired state and every bundle and reports,
//     --concurrency of them at the same moment;
//  4. console: members' browsers and an administrator's reading the common pages, five pages a
//     second each;
//  5. steady sync: computers sync at a fixed rate (desired state with If-None-Match, then the
//     report), one step per rate, with the administrator's console read twice a second.
//
// Between phases it waits until the server answers quickly again. Syncing every ten minutes,
// N people make N/600 syncs a second: the steady step that still answers well gives the number
// of people the machine carries.
//
// It prints a table per phase: requests, errors, p50/p95/p99 and the largest latency; with a
// container of its own also the memory: the highest the container reached (cgroup memory.peak)
// and docker stats samples.
//
// Every request of a person or computer carries an X-Forwarded-For with an address of its own,
// as they would behind the reverse proxy the server trusts; otherwise the per-address limits on
// sign-in would be what is measured.
//
// The task uses the standard library only, like the rest of tools/dev, and needs Docker unless
// --url points at a server that is already running (its first administrator is then created
// with --setup-token, so it has to be a fresh one).

type loadOptions struct {
	people, concurrency, uploads, repoSkills, browsers int
	url, setupToken, image, cpus, memory               string
	build, keep                                        bool
	steadyRates                                        []int
	step, consoleFor                                   time.Duration
	out                                                string
}

func runLoad(root string, args []string) error {
	o := loadOptions{}
	fs := flag.NewFlagSet("load", flag.ContinueOnError)
	fs.IntVar(&o.people, "people", 500, "people, each with one computer")
	fs.IntVar(&o.concurrency, "concurrency", 100, "computers syncing at the same moment in the first sync")
	fs.IntVar(&o.uploads, "uploads", 30, "skills uploaded and published through the API")
	fs.IntVar(&o.repoSkills, "repo-skills", 100, "skills in the imported resource repository")
	fs.StringVar(&o.url, "url", "", "a running server to test instead of starting a container")
	fs.StringVar(&o.setupToken, "setup-token", "", "the setup token of the server given with --url")
	fs.StringVar(&o.image, "image", "", "the server image to run (default: build one from this tree)")
	fs.BoolVar(&o.build, "build", false, "build the image from this tree even when --image is given")
	fs.StringVar(&o.cpus, "cpus", "2", "CPU limit of the container")
	fs.StringVar(&o.memory, "memory", "2g", "memory limit of the container (no swap)")
	fs.BoolVar(&o.keep, "keep", false, "leave the container running afterwards")
	rates := fs.String("steady-rates", "5,10,25,40,50", "syncs per second of the steady phase, one step each")
	fs.DurationVar(&o.step, "step", 30*time.Second, "how long each steady step lasts")
	fs.DurationVar(&o.consoleFor, "console", 30*time.Second, "how long the console phase lasts")
	fs.IntVar(&o.browsers, "browsers", 10, "members' browsers open at once in the console phase, a page each 200 ms")
	fs.StringVar(&o.out, "out", "", "also write the results as JSON to this file")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	for _, r := range strings.FieldsFunc(*rates, func(c rune) bool { return c == ',' }) {
		n, err := strconv.Atoi(strings.TrimSpace(r))
		if err != nil || n < 1 {
			return fmt.Errorf("--steady-rates: %q is not a rate", r)
		}
		o.steadyRates = append(o.steadyRates, n)
	}
	b, err := readBrand(root)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "load-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	// The resource repository, served over HTTPS on this machine for the server to import.
	repo, err := makeLoadRepo(work, b, o.repoSkills)
	if err != nil {
		return err
	}
	gitSrv, err := serveGit(work, repo)
	if err != nil {
		return err
	}
	defer gitSrv.Close()

	l := &loader{brand: b, opts: o, stats: newStats(), transport: &http.Transport{MaxIdleConns: 2048, MaxIdleConnsPerHost: 2048, IdleConnTimeout: time.Minute}}
	repoURL := gitSrv.url(repo)
	if o.url == "" {
		c, err := startLoadContainer(root, b, o, work)
		if err != nil {
			return err
		}
		if !o.keep {
			defer c.remove()
		}
		l.container = c
		l.base = c.base
		l.setupToken = c.setupToken
		repoURL = strings.Replace(repoURL, "127.0.0.1", "host.docker.internal", 1)
	} else {
		l.base = strings.TrimSuffix(o.url, "/")
		l.setupToken = o.setupToken
	}
	return l.run(repoURL)
}

// loader holds the organisation being played.
type loader struct {
	brand      brandNames
	opts       loadOptions
	base       string
	setupToken string
	transport  *http.Transport
	stats      *stats
	container  *loadContainer
	admin      *person
	people     []*person
	results    []phaseResult
}

// person is someone with a browser session and one computer.
type person struct {
	n        int
	username string
	addr     string // X-Forwarded-For
	browser  *http.Client
	token    string // the computer's device token
	etag     string // of its last desired state
	cache    map[string]bool
}

type phaseResult struct {
	Phase    string         `json:"phase"`
	Seconds  float64        `json:"seconds"`
	Rows     []statRow      `json:"rows"`
	MemoryMB map[string]int `json:"memory_mb,omitempty"`
}

func (l *loader) run(repoURL string) error {
	ctx := context.Background()
	if err := waitReady(l.base+"/readyz", 2*time.Minute); err != nil {
		return err
	}
	if l.container != nil {
		l.container.sample(ctx)
	}
	fmt.Printf("server %s, %d people\n", l.base, l.opts.people)

	if err := l.phase("onboarding", l.onboard); err != nil {
		return err
	}
	if err := l.phase("publishing", func() error { return l.publish(repoURL) }); err != nil {
		return err
	}
	if err := l.phase("first sync", l.firstSync); err != nil {
		return err
	}
	l.settle()
	if err := l.phase(fmt.Sprintf("console: %d members and an administrator, 5 pages/s each", min(l.opts.browsers, len(l.people))), l.console); err != nil {
		return err
	}
	for _, rate := range l.opts.steadyRates {
		l.settle()
		if err := l.phase(fmt.Sprintf("steady sync %d/s + console 2/s", rate), func() error { return l.steady(rate) }); err != nil {
			return err
		}
	}
	if l.container != nil {
		l.container.stopSampling()
		peak, err := l.container.peak()
		if err != nil {
			fmt.Println("memory.peak:", err)
		}
		fmt.Printf("\nmemory: container peak %d MB (cgroup memory.peak), highest docker stats sample %d MB, after the run %d MB\n",
			peak>>20, l.container.maxMem.Load()>>20, l.container.lastMem.Load()>>20)
		l.results = append(l.results, phaseResult{Phase: "memory", MemoryMB: map[string]int{
			"peak": int(peak >> 20), "max_sample": int(l.container.maxMem.Load() >> 20), "end": int(l.container.lastMem.Load() >> 20)}})
	}
	if l.opts.out != "" {
		data, _ := json.MarshalIndent(l.results, "", "  ")
		return os.WriteFile(l.opts.out, data, 0o644)
	}
	return nil
}

// settle waits until the server answers its readiness check quickly again, so that one phase's
// backlog is not measured in the next.
func (l *loader) settle() {
	for deadline := time.Now().Add(5 * time.Minute); time.Now().Before(deadline); time.Sleep(2 * time.Second) {
		start := time.Now()
		resp, err := http.Get(l.base + "/readyz")
		if err == nil {
			resp.Body.Close()
			if time.Since(start) < 50*time.Millisecond {
				return
			}
		}
	}
}

// phase runs one phase with fresh statistics and prints its table.
func (l *loader) phase(name string, f func() error) error {
	l.stats = newStats()
	var memBefore int64
	if l.container != nil {
		memBefore = l.container.lastMem.Load()
		l.container.maxMem.Store(memBefore)
	}
	start := time.Now()
	err := f()
	took := time.Since(start)
	rows := l.stats.rows()
	fmt.Printf("\n== %s (%.1fs)\n", name, took.Seconds())
	printRows(rows)
	res := phaseResult{Phase: name, Seconds: took.Seconds(), Rows: rows}
	if l.container != nil {
		res.MemoryMB = map[string]int{"before": int(memBefore >> 20), "max_sample": int(l.container.maxMem.Load() >> 20)}
		fmt.Printf("memory (docker stats): %d MB before, at most %d MB during\n", memBefore>>20, l.container.maxMem.Load()>>20)
	}
	l.results = append(l.results, res)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// ---- the people

func (l *loader) newBrowser() *http.Client {
	jar, _ := cookiejar.New(nil)
	return &http.Client{Jar: jar, Transport: l.transport, Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// addrOf is the client address of the nth person, as the reverse proxy would pass it on.
func addrOf(n int) string { return fmt.Sprintf("10.%d.%d.%d", 1+n>>16, n>>8&255, 1+n&253) }

// call makes one request, records its latency under name and fails unless the status is one of
// want. body is sent as JSON unless it is already []byte.
func (l *loader) call(c *http.Client, name, addr, method, path string, body any, header map[string]string, out any, want ...int) (*http.Response, error) {
	var rd io.Reader
	ctype := "application/json"
	switch v := body.(type) {
	case nil:
	case []byte:
		rd, ctype = bytes.NewReader(v), "application/octet-stream"
	default:
		data, err := json.Marshal(v)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, l.base+path, rd)
	if err != nil {
		return nil, err
	}
	if rd != nil {
		req.Header.Set("Content-Type", ctype)
	}
	if addr != "" {
		req.Header.Set("X-Forwarded-For", addr)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := c.Do(req)
	var data []byte
	if err == nil {
		data, err = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	took := time.Since(start)
	if err == nil && !slices.Contains(want, resp.StatusCode) {
		err = fmt.Errorf("%s %s: %d %s", method, path, resp.StatusCode, bytes.TrimSpace(data[:min(len(data), 300)]))
	}
	if err == nil && out != nil && len(data) > 0 {
		err = json.Unmarshal(data, out)
	}
	l.stats.add(name, took, err)
	return resp, err
}

func (l *loader) device(p *person) *http.Client {
	return &http.Client{Transport: l.transport, Timeout: 60 * time.Second}
}

func (l *loader) deviceHeader(p *person, extra ...string) map[string]string {
	h := map[string]string{"Authorization": "Bearer " + p.token}
	for i := 0; i+1 < len(extra); i += 2 {
		h[extra[i]] = extra[i+1]
	}
	return h
}

// onboard creates the first administrator, a product, and every person: an invitation, its
// acceptance (a local account with a password), and one computer signed in with the device
// code flow, approved in that person's browser.
func (l *loader) onboard() error {
	l.admin = &person{username: "admin", addr: "10.0.0.2", browser: l.newBrowser()}
	if _, err := l.call(l.admin.browser, "setup admin", l.admin.addr, http.MethodPost, "/api/v1/setup/admin",
		map[string]any{"username": "admin", "password": "<placeholder: load admin password>"},
		map[string]string{"X-Setup-Token": l.setupToken}, nil, http.StatusCreated); err != nil {
		return err
	}
	if _, err := l.call(l.admin.browser, "product create", l.admin.addr, http.MethodPost, "/api/v1/products",
		map[string]any{"code": "shop", "name": "Shop"}, nil, nil, http.StatusCreated); err != nil {
		return err
	}
	var links []string
	for from := 0; from < l.opts.people; from += 100 {
		var emails []string
		for i := from; i < min(from+100, l.opts.people); i++ {
			emails = append(emails, fmt.Sprintf("person%04d@example.com", i))
		}
		var inv struct{ Items []struct{ Link string } }
		if _, err := l.call(l.admin.browser, "invites create (100)", l.admin.addr, http.MethodPost, "/api/v1/invites",
			map[string]any{"emails": emails, "products": []string{"shop"}}, nil, &inv, http.StatusCreated); err != nil {
			return err
		}
		for _, it := range inv.Items {
			links = append(links, it.Link)
		}
	}
	l.people = make([]*person, len(links))
	err := parallel(8, len(links), func(i int) error {
		p := &person{n: i, username: fmt.Sprintf("person%04d", i), addr: addrOf(i), browser: l.newBrowser(), cache: map[string]bool{}}
		l.people[i] = p
		token := links[i][strings.LastIndex(links[i], "/")+1:]
		if _, err := l.call(p.browser, "invite accept", p.addr, http.MethodPost, "/api/v1/auth/invites/"+token+"/accept",
			map[string]any{"username": p.username, "display_name": "Person " + strconv.Itoa(i), "password": "<placeholder: load password>"},
			nil, nil, http.StatusCreated); err != nil {
			return err
		}
		return l.signInComputer(p)
	})
	if err != nil {
		return err
	}
	// The first person owns the product.
	var me struct{ ID string }
	if _, err := l.call(l.people[0].browser, "me", l.people[0].addr, http.MethodGet, "/api/v1/me", nil, nil, &me, http.StatusOK); err != nil {
		return err
	}
	_, err = l.call(l.admin.browser, "product member put", l.admin.addr, http.MethodPut, "/api/v1/products/shop/members/"+me.ID,
		map[string]any{"role": "owner"}, nil, nil, http.StatusOK)
	return err
}

// signInComputer is rgd login on the person's computer, approved in their browser.
func (l *loader) signInComputer(p *person) error {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	dev := &http.Client{Transport: l.transport, Timeout: 60 * time.Second}
	var code struct {
		DeviceCode string `json:"device_code"`
		UserCode   string `json:"user_code"`
	}
	if _, err := l.call(dev, "device code", p.addr, http.MethodPost, "/api/v1/device/code", map[string]any{
		"name": "laptop-" + strconv.Itoa(p.n), "os": "linux", "arch": "amd64", "cli_version": "0.1.0",
		"public_key": base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()), "key_storage": "file",
	}, nil, &code, http.StatusOK); err != nil {
		return err
	}
	if _, err := l.call(p.browser, "device approve", p.addr, http.MethodPost, "/api/v1/device/requests/"+code.UserCode+"/approve",
		nil, nil, nil, http.StatusNoContent); err != nil {
		return err
	}
	var tok struct {
		DeviceToken string `json:"device_token"`
	}
	if _, err := l.call(dev, "device token", p.addr, http.MethodPost, "/api/v1/device/token",
		map[string]any{"device_code": code.DeviceCode}, nil, &tok, http.StatusOK); err != nil {
		return err
	}
	p.token = tok.DeviceToken
	return nil
}

// publish uploads skills one after another and publishes each, then imports the resource
// repository and publishes everything it brought.
func (l *loader) publish(repoURL string) error {
	a := l.admin
	for i := range l.opts.uploads {
		code := fmt.Sprintf("upload-%03d", i)
		bundle, err := skillArchive(code, 8<<10)
		if err != nil {
			return err
		}
		resp, err := l.call(a.browser, "upload draft", a.addr, http.MethodPut, "/api/v1/resources/"+code+"/draft/bundle?kind=skill", bundle, nil, nil, http.StatusCreated)
		if err != nil {
			return err
		}
		if _, err := l.call(a.browser, "publish", a.addr, http.MethodPost, "/api/v1/resources/"+code+"/publish", map[string]any{},
			map[string]string{"If-Match": resp.Header.Get("ETag")}, nil, http.StatusCreated); err != nil {
			return err
		}
	}
	var src struct {
		ID       string `json:"id"`
		LastPull *struct {
			Result   string `json:"result"`
			Imported int    `json:"imported"`
			Failed   int    `json:"failed"`
		} `json:"last_pull"`
	}
	start := time.Now()
	if _, err := l.call(a.browser, "source create", a.addr, http.MethodPost, "/api/v1/sources",
		map[string]any{"name": "resources", "url": repoURL, "branch": "main"}, nil, &src, http.StatusCreated); err != nil {
		return err
	}
	for src.LastPull == nil || src.LastPull.Result == "running" {
		if time.Since(start) > 10*time.Minute {
			return errors.New("the import did not finish in 10 minutes")
		}
		time.Sleep(250 * time.Millisecond)
		if _, err := l.call(a.browser, "source get", a.addr, http.MethodGet, "/api/v1/sources/"+src.ID, nil, nil, &src, http.StatusOK); err != nil {
			return err
		}
	}
	l.stats.add("import (whole pull, by polling)", time.Since(start), nil)
	if src.LastPull.Result != "ok" {
		return fmt.Errorf("the import: %+v", *src.LastPull)
	}
	fmt.Printf("imported %d resources (%d failed) in %.1fs\n", src.LastPull.Imported, src.LastPull.Failed, time.Since(start).Seconds())
	var pending struct{ Items []struct{ Code string } }
	if _, err := l.call(a.browser, "resources list", a.addr, http.MethodGet, "/api/v1/resources?pending=true", nil, nil, &pending, http.StatusOK); err != nil {
		return err
	}
	return parallel(4, len(pending.Items), func(i int) error {
		code := pending.Items[i].Code
		resp, err := l.call(a.browser, "draft get", a.addr, http.MethodGet, "/api/v1/resources/"+code+"/draft", nil, nil, nil, http.StatusOK)
		if err != nil {
			return err
		}
		_, err = l.call(a.browser, "publish", a.addr, http.MethodPost, "/api/v1/resources/"+code+"/publish", map[string]any{},
			map[string]string{"If-Match": resp.Header.Get("ETag")}, nil, http.StatusCreated)
		return err
	})
}

// desired is the part of the desired state a computer acts on.
type desired struct {
	Resources []struct {
		Code    string `json:"code"`
		Version int    `json:"version"`
		Bundle  string `json:"bundle"`
	} `json:"resources"`
}

// syncOnce is one rgd sync of p's computer: the desired state (with If-None-Match after the
// first), the bundles it does not have yet, and the report.
func (l *loader) syncOnce(p *person) error {
	c := l.device(p)
	h := l.deviceHeader(p)
	if p.etag != "" {
		h["If-None-Match"] = p.etag
	}
	var st desired
	resp, err := l.call(c, "sync: desired state", p.addr, http.MethodGet, "/api/v1/sync/desired", nil, h, &st, http.StatusOK, http.StatusNotModified)
	if err != nil {
		return err
	}
	p.etag = resp.Header.Get("ETag")
	installs := []map[string]any{}
	for _, r := range st.Resources {
		if r.Bundle != "" && !p.cache[r.Bundle] {
			if _, err := l.call(c, "sync: bundle", p.addr, http.MethodGet, "/api/v1/sync/bundles/"+r.Bundle, nil, l.deviceHeader(p), nil, http.StatusOK); err != nil {
				return err
			}
			p.cache[r.Bundle] = true
		}
		for _, tool := range []string{"claude-code", "codex"} {
			installs = append(installs, map[string]any{"code": r.Code, "version": r.Version, "tool": tool, "status": "installed"})
		}
	}
	if resp.StatusCode == http.StatusNotModified {
		installs = nil // nothing changed: a real computer reports what it has, which the server already knows
	}
	report := map[string]any{"cli_version": "0.1.0", "os": "linux", "arch": "amd64", "finished_at": time.Now().UTC().Format(time.RFC3339),
		"result": "ok", "tools": []map[string]any{{"tool": "claude-code", "version": "2.1.330"}, {"tool": "codex", "version": "0.172.0"}},
		"installs": installs}
	if installs == nil {
		report["installs"] = []any{}
	}
	_, err = l.call(c, "sync: report", p.addr, http.MethodPost, "/api/v1/sync/report", report, l.deviceHeader(p), nil, http.StatusNoContent, http.StatusOK)
	return err
}

// firstSync is every computer's first sync, opts.concurrency at a time.
func (l *loader) firstSync() error {
	start := time.Now()
	var failed atomic.Int64
	_ = parallel(l.opts.concurrency, len(l.people), func(i int) error {
		t := time.Now()
		err := l.syncOnce(l.people[i])
		l.stats.add("whole sync", time.Since(t), err)
		if err != nil {
			failed.Add(1)
		}
		return nil
	})
	fmt.Printf("%d computers synced in %.1fs (%.1f syncs/s), %d failed\n", len(l.people), time.Since(start).Seconds(),
		float64(len(l.people))/time.Since(start).Seconds(), failed.Load())
	return nil
}

// steady starts rate syncs per second, round robin over the computers, for opts.step, whatever
// the answers take (an open loop: slow answers do not slow the arrivals down). The console is
// used twice a second at the same time.
func (l *loader) steady(rate int) error {
	var wg sync.WaitGroup
	stop := time.Now().Add(l.opts.step)
	next := 0
	tick := time.NewTicker(time.Second / time.Duration(rate))
	defer tick.Stop()
	consoleTick := time.NewTicker(500 * time.Millisecond)
	defer consoleTick.Stop()
	var n int
	for time.Now().Before(stop) {
		select {
		case <-tick.C:
			p := l.people[next%len(l.people)]
			next++
			wg.Add(1)
			go func() {
				defer wg.Done()
				t := time.Now()
				err := l.syncOnce(p)
				l.stats.add("whole sync", time.Since(t), err)
			}()
		case <-consoleTick.C:
			page := consolePages[n%len(consolePages)]
			n++
			wg.Add(1)
			go func() {
				defer wg.Done()
				l.call(l.admin.browser, "console (admin): "+page, l.admin.addr, http.MethodGet, page, nil, nil, nil, http.StatusOK)
			}()
		}
	}
	wg.Wait()
	return nil
}

// consolePages are what the console reads on the administrator's common pages.
var consolePages = []string{
	"/api/v1/me", "/api/v1/catalog", "/api/v1/resources", "/api/v1/installs/summary", "/api/v1/users",
	"/api/v1/products", "/api/v1/audit?limit=50", "/api/v1/feedback", "/api/v1/me/notifications", "/api/v1/adapters",
}

// memberPages are what the console reads on a member's common pages.
var memberPages = []string{
	"/api/v1/me", "/api/v1/catalog", "/api/v1/catalog/upload-000", "/api/v1/me/devices", "/api/v1/me/secrets",
	"/api/v1/products", "/api/v1/feedback?mine=true", "/api/v1/me/notifications",
}

// console is opts.browsers members' browsers and the administrator's, each reading a page every
// 200 ms (the per-account limit is 20 a second), for opts.consoleFor.
func (l *loader) console() error {
	stop := time.Now().Add(l.opts.consoleFor)
	var wg sync.WaitGroup
	browse := func(p *person, pages []string, prefix string) {
		defer wg.Done()
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for i := p.n; time.Now().Before(stop); i++ {
			page := pages[i%len(pages)]
			l.call(p.browser, prefix+page, p.addr, http.MethodGet, page, nil, nil, nil, http.StatusOK)
			<-tick.C
		}
	}
	wg.Add(1)
	go browse(l.admin, consolePages, "console (admin): ")
	for i := range min(l.opts.browsers, len(l.people)) {
		wg.Add(1)
		go browse(l.people[i], memberPages, "console (member): ")
	}
	wg.Wait()
	return nil
}

// parallel runs f(0..n-1), at most k at a time, and returns the first error.
func parallel(k, n int, f func(int) error) error {
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		first error
	)
	sem := make(chan struct{}, k)
	for i := range n {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			if err := f(i); err != nil {
				mu.Lock()
				if first == nil {
					first = err
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	return first
}

// ---- statistics

type stats struct {
	mu   sync.Mutex
	lat  map[string][]time.Duration
	errs map[string]int
	last map[string]string
}

func newStats() *stats {
	return &stats{lat: map[string][]time.Duration{}, errs: map[string]int{}, last: map[string]string{}}
}

func (s *stats) add(name string, d time.Duration, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lat[name] = append(s.lat[name], d)
	if err != nil {
		s.errs[name]++
		s.last[name] = err.Error()
	}
}

type statRow struct {
	Name   string  `json:"name"`
	Count  int     `json:"count"`
	Errors int     `json:"errors"`
	P50    float64 `json:"p50_ms"`
	P95    float64 `json:"p95_ms"`
	P99    float64 `json:"p99_ms"`
	Max    float64 `json:"max_ms"`
	Error  string  `json:"last_error,omitempty"`
}

func (s *stats) rows() []statRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []statRow
	for name, ds := range s.lat {
		sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
		q := func(f float64) float64 {
			return float64(ds[min(len(ds)-1, int(f*float64(len(ds))))].Microseconds()) / 1000
		}
		out = append(out, statRow{Name: name, Count: len(ds), Errors: s.errs[name], P50: q(0.50), P95: q(0.95), P99: q(0.99),
			Max: float64(ds[len(ds)-1].Microseconds()) / 1000, Error: s.last[name]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func printRows(rows []statRow) {
	fmt.Printf("%-48s %7s %6s %9s %9s %9s %9s\n", "", "count", "errors", "p50 ms", "p95 ms", "p99 ms", "max ms")
	for _, r := range rows {
		fmt.Printf("%-48s %7d %6d %9.1f %9.1f %9.1f %9.1f\n", r.Name, r.Count, r.Errors, r.P50, r.P95, r.P99, r.Max)
		if r.Error != "" {
			fmt.Printf("    last error: %s\n", r.Error)
		}
	}
}

// ---- the resources

// skillArchive is a gzipped tar of one skill directory, about size bytes of text.
func skillArchive(code string, size int) ([]byte, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := skillFiles(code, size)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		data := files[name]
		if err := tw.WriteHeader(&tar.Header{Name: code + "/" + name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			return nil, err
		}
		if _, err := tw.Write([]byte(data)); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// skillFiles are the files of a skill of about size bytes: SKILL.md, meta.yaml and a reference.
func skillFiles(code string, size int) map[string]string {
	line := "Check the change against the team's rules and say what to fix, one point per line.\n"
	body := strings.Repeat(line, max(1, size/2/len(line)))
	return map[string]string{
		"SKILL.md":           "---\nname: " + code + "\ndescription: A skill of the load test, " + code + ".\n---\n\n" + body,
		"meta.yaml":          "name: " + code + "\nwhen: When the load test needs it\n",
		"references/rule.md": "# Rules\n\n" + body,
	}
}

// makeLoadRepo writes a resource repository with n skills and commits it as a bare repository
// under work; it returns the bare repository's path relative to work.
func makeLoadRepo(work string, b brandNames, n int) (string, error) {
	src := filepath.Join(work, "repo-src")
	files := map[string]string{b.RepoConfig: "schema: 1\nname: Load test resources\nlocale: en\ndefaults:\n  scope: org\n  policy: recommended\n  audience: rd\n"}
	for i := range n {
		code := fmt.Sprintf("repo-skill-%03d", i)
		for name, data := range skillFiles(code, 4<<10+(i%8)<<10) {
			files["skills/"+code+"/"+name] = data
		}
	}
	for name, data := range files {
		p := filepath.Join(src, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			return "", err
		}
	}
	git := func(dir string, args ...string) error {
		base := []string{"-c", "user.name=Load", "-c", "user.email=load@example.com", "-c", "commit.gpgsign=false", "-c", "init.defaultBranch=main", "-c", "core.autocrlf=false"}
		cmd := exec.Command("git", append(base, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
		}
		return nil
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"commit", "-q", "-m", "resources"}} {
		if err := git(src, args...); err != nil {
			return "", err
		}
	}
	if err := git(work, "clone", "-q", "--bare", src, "resources.git"); err != nil {
		return "", err
	}
	return "resources.git", nil
}

// ---- the container

type loadContainer struct {
	name, network, base, setupToken string
	maxMem, lastMem                 atomic.Int64
	cancel                          context.CancelFunc
	done                            chan struct{}
}

const (
	loadName   = "rigdepot-load"
	loadSubnet = "172.31.249.0/24"
)

// startLoadContainer runs the server image limited to opts.cpus and opts.memory on a network of
// its own, with the git server's certificate mounted, and waits until it answers.
func startLoadContainer(root string, b brandNames, o loadOptions, work string) (*loadContainer, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, errors.New("docker was not found; give --url of a running server instead")
	}
	image := o.image
	if image == "" || o.build {
		if image == "" {
			image = b.CLILong + "-server:load"
		}
		fmt.Println("==> docker build -t", image, ".")
		cmd := exec.Command("docker", "build", "-t", image, ".")
		cmd.Dir, cmd.Stdout, cmd.Stderr = root, os.Stdout, os.Stderr
		if err := cmd.Run(); err != nil {
			return nil, err
		}
	}
	token := make([]byte, 18)
	rand.Read(token)
	c := &loadContainer{name: loadName, network: loadName, setupToken: base64.RawURLEncoding.EncodeToString(token)}
	if out, err := exec.Command("docker", "network", "create", "--subnet", loadSubnet, c.network).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker network create: %v\n%s", err, out)
	}
	env := b.EnvPrefix
	args := []string{"run", "--detach", "--name", c.name, "--network", c.network,
		"--cpus", o.cpus, "--memory", o.memory, "--memory-swap", o.memory,
		"--read-only", "--tmpfs", "/tmp", "--publish", "127.0.0.1::8080",
		"--add-host", "host.docker.internal:host-gateway",
		"--volume", work + "/tls:/load:ro",
		"--env", env + "BASE_URL=http://localhost:8080",
		"--env", env + "TRUSTED_PROXIES=" + loadSubnet + ",192.168.0.0/16,172.16.0.0/12",
		"--env", env + "SETUP_TOKEN=" + c.setupToken,
		"--env", "GIT_SSL_CAINFO=/load/ca.pem",
		image, "serve"}
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		c.remove()
		return nil, fmt.Errorf("docker run: %v\n%s", err, out)
	}
	out, err := exec.Command("docker", "port", c.name, "8080/tcp").Output()
	if err != nil {
		c.remove()
		return nil, err
	}
	c.base = "http://" + strings.TrimSpace(strings.Split(string(out), "\n")[0])
	fmt.Printf("container %s: %s, --cpus %s --memory %s\n", c.name, image, o.cpus, o.memory)
	return c, nil
}

// remove removes the container with its anonymous data volume, and the network: what
// startLoadContainer made, and nothing else.
func (c *loadContainer) remove() {
	if c.cancel != nil {
		c.stopSampling()
	}
	exec.Command("docker", "rm", "--force", "--volumes", c.name).Run()
	exec.Command("docker", "network", "rm", c.network).Run()
}

// sample reads the container's memory with docker stats until stopSampling.
func (c *loadContainer) sample(ctx context.Context) {
	ctx, c.cancel = context.WithCancel(ctx)
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		for ctx.Err() == nil {
			out, err := exec.CommandContext(ctx, "docker", "stats", "--no-stream", "--format", "{{.MemUsage}}", c.name).Output()
			if err == nil {
				used, _, _ := strings.Cut(strings.TrimSpace(string(out)), "/")
				if n, ok := parseSize(strings.TrimSpace(used)); ok {
					c.lastMem.Store(n)
					if n > c.maxMem.Load() {
						c.maxMem.Store(n)
					}
				}
			}
		}
	}()
}

func (c *loadContainer) stopSampling() {
	c.cancel()
	<-c.done
}

// peak is the highest memory use of the container (cgroup v2 memory.peak).
func (c *loadContainer) peak() (int64, error) {
	out, err := exec.Command("docker", "exec", c.name, "cat", "/sys/fs/cgroup/memory.peak").Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
}

// parseSize reads a size as docker stats prints it: 41.2MiB, 1.03GiB.
func parseSize(s string) (int64, bool) {
	units := []struct {
		suffix string
		mult   float64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"GB", 1e9}, {"MB", 1e6}, {"kB", 1e3}, {"B", 1}}
	for _, u := range units {
		if strings.HasSuffix(s, u.suffix) {
			f, err := strconv.ParseFloat(strings.TrimSuffix(s, u.suffix), 64)
			return int64(f * u.mult), err == nil
		}
	}
	return 0, false
}

// ---- the git server

type gitServer struct {
	srv  *http.Server
	ln   net.Listener
	root string
}

func (g *gitServer) Close()                 { g.srv.Close() }
func (g *gitServer) url(repo string) string { return "https://" + g.ln.Addr().String() + "/" + repo }
