package main

import (
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/makkalot/caretz"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/tools"
)

//go:embed web/*
var web embed.FS

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, 404, "API endpoint not found")
	})
	assets, _ := fs.Sub(web, "web")
	mux.Handle("GET /", http.FileServer(http.FS(assets)))
	mux.HandleFunc("GET /api/projects", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		list := a.listLocked()
		a.mu.Unlock()
		writeJSON(w, http.StatusOK, list)
	})
	mux.HandleFunc("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Name string `json:"name"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		body.Name = strings.TrimSpace(body.Name)
		if body.Name == "" || len(body.Name) > 120 {
			fail(w, 400, "name must be between 1 and 120 bytes")
			return
		}
		p, err := a.create(body.Name)
		if err != nil {
			fail(w, 500, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, p)
	})
	mux.HandleFunc("POST /api/projects/{id}/messages", a.withProject(func(w http.ResponseWriter, r *http.Request, p *project) {
		var body struct {
			Text string `json:"text"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.Text) == "" {
			fail(w, 400, "text is required")
			return
		}
		a.acceptWork(w, a.start(p, body.Text))
	}))
	mux.HandleFunc("POST /api/projects/{id}/run", a.withProject(func(w http.ResponseWriter, r *http.Request, p *project) {
		a.acceptWork(w, a.startCommand(p, defaultRunCommand))
	}))
	mux.HandleFunc("POST /api/projects/{id}/commands", a.withProject(func(w http.ResponseWriter, r *http.Request, p *project) {
		var body struct {
			Command string `json:"command"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		if strings.TrimSpace(body.Command) == "" || len(body.Command) > 8192 || strings.ContainsRune(body.Command, '\x00') {
			fail(w, 400, "command must be between 1 and 8192 bytes, without NUL characters")
			return
		}
		a.acceptWork(w, a.startCommand(p, body.Command))
	}))
	mux.HandleFunc("POST /api/projects/{id}/cancel", a.withProject(func(w http.ResponseWriter, r *http.Request, p *project) {
		p.cancel()
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /api/projects/{id}/history", a.withProject(func(w http.ResponseWriter, r *http.Request, p *project) {
		writeJSON(w, 200, history(p))
	}))
	mux.HandleFunc("GET /api/projects/{id}/permissions", a.withProject(func(w http.ResponseWriter, r *http.Request, p *project) {
		writeJSON(w, 200, p.session.PendingPermissions())
	}))
	mux.HandleFunc("POST /api/projects/{id}/permissions/{reqID}", a.withProject(func(w http.ResponseWriter, r *http.Request, p *project) {
		var body struct {
			Decision string `json:"decision"`
		}
		if !readJSON(w, r, &body) {
			return
		}
		switch body.Decision {
		case eventing.PermissionDecisionAllow, eventing.PermissionDecisionAllowSession, eventing.PermissionDecisionDeny:
		default:
			fail(w, 400, "decision must be allow, allow_session, or deny")
			return
		}
		if err := p.session.ResolvePermission(caretz.PermissionResolution{
			RequestID: r.PathValue("reqID"), Decision: body.Decision, Resolver: "coderun-ui",
		}); err != nil {
			fail(w, 409, err.Error())
			return
		}
		writeJSON(w, 200, map[string]bool{"ok": true})
	}))
	mux.HandleFunc("GET /api/projects/{id}/files", a.withProject(listFiles))
	mux.HandleFunc("GET /api/projects/{id}/files/{path...}", a.withProject(readFile))
	mux.HandleFunc("GET /api/projects/{id}/events", a.withProject(a.events))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; object-src 'none'; frame-ancestors 'none'")
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasPrefix(r.URL.Path, "/api/") {
			for _, component := range strings.Split(r.URL.Path, "/") {
				if component == "." || component == ".." {
					fail(w, 400, "invalid API path")
					return
				}
			}
		}
		// A page on another origin must not control this local agent.
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			fail(w, 403, "cross-site requests are not allowed")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
				fail(w, 403, "origin does not match this server")
				return
			}
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *app) withProject(fn func(http.ResponseWriter, *http.Request, *project)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, err := a.open(r.PathValue("id"))
		if err != nil {
			status := 400
			if errors.Is(err, os.ErrNotExist) {
				status = 404
			} else if errors.Is(err, errClosing) {
				status = 503
			}
			fail(w, status, err.Error())
			return
		}
		fn(w, r, p)
	}
}

func (a *app) acceptWork(w http.ResponseWriter, err error) {
	if err != nil {
		status := 500
		if errors.Is(err, errBusy) {
			status = 409
		} else if errors.Is(err, errClosing) {
			status = 503
		}
		fail(w, status, err.Error())
		return
	}
	writeJSON(w, 202, map[string]bool{"accepted": true})
}

type chatMessage struct {
	Role llm.Role `json:"role"`
	Text string   `json:"text"`
}

func history(p *project) []chatMessage {
	result := make([]chatMessage, 0)
	for _, m := range p.session.Messages() {
		if (m.Role == llm.RoleUser || m.Role == llm.RoleAssistant) && m.Content != "" {
			result = append(result, chatMessage{m.Role, m.Content})
		}
	}
	return result
}

const maxFileBytes = 256 * 1024

func listFiles(w http.ResponseWriter, r *http.Request, p *project) {
	root, err := os.OpenRoot(p.Dir)
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	defer root.Close()
	files := make([]string, 0)
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if ignoredProjectEntry(root, name, entry) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.Type().IsRegular() {
			files = append(files, name)
		}
		if len(files) >= 500 {
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		fail(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, files)
}

func ignoredProjectEntry(root *os.Root, name string, entry fs.DirEntry) bool {
	base := entry.Name()
	if strings.HasPrefix(base, ".") {
		return true
	}
	if entry.IsDir() {
		switch base {
		case "venv", "env", "__pycache__", "__pypackages__", "node_modules",
			"site-packages", "build", "dist", "tmp", "temp", "htmlcov",
			"pip-cache", "pip_cache", "pip-wheel-metadata":
			return true
		}
		if strings.HasSuffix(base, ".egg-info") || strings.HasSuffix(base, ".dist-info") ||
			name == "Library/Caches" || strings.HasSuffix(name, "/Library/Caches") {
			return true
		}
		// Virtual environments can have arbitrary names. Detect their marker
		// before walking thousands of installed package files.
		if info, err := root.Stat(path.Join(name, "pyvenv.cfg")); err == nil && info.Mode().IsRegular() {
			return true
		}
		return false
	}
	switch strings.ToLower(path.Ext(base)) {
	case ".pyc", ".pyo", ".tmp", ".temp", ".swp", ".swo":
		return true
	}
	return strings.HasSuffix(base, "~")
}

func readFile(w http.ResponseWriter, r *http.Request, p *project) {
	name := r.PathValue("path")
	if !fs.ValidPath(name) || strings.Contains(name, "\\") {
		fail(w, 400, "invalid file path")
		return
	}
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			fail(w, 400, "hidden files are not available")
			return
		}
	}
	full := filepath.Join(p.Dir, filepath.FromSlash(name))
	if safe, reason := tools.IsPathSafe(full, p.Dir); !safe {
		fail(w, 400, reason)
		return
	}
	root, err := os.OpenRoot(p.Dir)
	if err != nil {
		fail(w, 404, "project directory unavailable")
		return
	}
	defer root.Close()
	info, err := root.Stat(name)
	if err != nil {
		fail(w, 404, "file not found")
		return
	}
	if !info.Mode().IsRegular() {
		fail(w, 400, "only regular files are available")
		return
	}
	f, err := root.Open(name)
	if err != nil {
		fail(w, 404, "file not found")
		return
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		fail(w, 400, "only regular files are available")
		return
	}
	contents, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if err != nil {
		fail(w, 500, "could not read file")
		return
	}
	if len(contents) > maxFileBytes {
		fail(w, 413, "file exceeds 256 KB")
		return
	}
	if tools.IsBinaryContent(contents, path.Base(name)) {
		fail(w, 415, "binary files are not available")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(contents)
}

func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		fail(w, 400, "invalid JSON: "+err.Error())
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		fail(w, 400, "expected one JSON object")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func fail(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
