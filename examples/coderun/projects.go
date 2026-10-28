package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/makkalot/caretz"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/tools"
	"github.com/makkalot/caretz/lib/types"
)

var validID = regexp.MustCompile(`^[a-z0-9-]{1,64}$`)
var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)
var errBusy = errors.New("a turn is already running")
var errClosing = errors.New("server is stopping")

type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Dir       string    `json:"-"`
	CreatedAt time.Time `json:"created_at"`
}

type turnState struct {
	Kind      string                    `json:"kind"`
	Running   bool                      `json:"running"`
	Revision  uint64                    `json:"revision"`
	Text      string                    `json:"text,omitempty"`
	StartedAt time.Time                 `json:"started_at"`
	Command   string                    `json:"command,omitempty"`
	Result    *tools.VerificationResult `json:"result,omitempty"`
}

type project struct {
	Project
	session     *caretz.Session
	mu          sync.Mutex
	state       turnState
	stop        chan struct{}
	cancelTurn  context.CancelFunc
	once        sync.Once
	liveText    string
	liveVersion uint64
}

type app struct {
	ctx      context.Context
	data     string
	client   *caretz.Client
	mu       sync.Mutex
	projects map[string]*project
	closing  bool
	turns    sync.WaitGroup
}

func newApp(ctx context.Context, data string, client *caretz.Client) (*app, error) {
	a := &app{ctx: ctx, data: data, client: client, projects: map[string]*project{}}
	if err := os.MkdirAll(filepath.Join(data, "projects"), 0700); err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(filepath.Join(data, "projects.json"))
	if errors.Is(err, os.ErrNotExist) {
		return a, nil
	}
	if err != nil {
		return nil, err
	}
	var projects []Project
	if err := json.Unmarshal(contents, &projects); err != nil {
		return nil, fmt.Errorf("read project registry: %w", err)
	}
	for _, p := range projects {
		if !validID.MatchString(p.ID) || a.projects[p.ID] != nil {
			return nil, fmt.Errorf("invalid or duplicate project ID %q", p.ID)
		}
		p.Dir = filepath.Join(data, "projects", p.ID)
		if safe, reason := tools.IsPathSafe(p.Dir, filepath.Join(data, "projects")); !safe {
			return nil, fmt.Errorf("project %s: %s", p.ID, reason)
		}
		a.projects[p.ID] = &project{Project: p}
	}
	return a, nil
}

func (a *app) listLocked() []Project {
	list := make([]Project, 0, len(a.projects))
	for _, p := range a.projects {
		list = append(list, p.Project)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].CreatedAt.Before(list[j].CreatedAt) })
	return list
}

func (a *app) create(name string) (Project, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return Project{}, errClosing
	}
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return Project{}, err
	}
	slug := strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if slug == "" {
		slug = "project"
	}
	if len(slug) > 50 {
		slug = strings.TrimRight(slug[:50], "-")
	}
	p := Project{ID: slug + "-" + hex.EncodeToString(suffix[:]), Name: name, CreatedAt: time.Now().UTC()}
	p.Dir = filepath.Join(a.data, "projects", p.ID)
	if err := os.Mkdir(p.Dir, 0700); err != nil {
		return Project{}, err
	}
	a.projects[p.ID] = &project{Project: p}
	contents, err := json.MarshalIndent(a.listLocked(), "", "  ")
	if err == nil {
		err = a.saveRegistry(contents)
	}
	if err != nil {
		delete(a.projects, p.ID)
		_ = os.Remove(p.Dir)
		return Project{}, err
	}
	return p, nil
}

func (a *app) saveRegistry(contents []byte) error {
	f, err := os.CreateTemp(a.data, ".projects-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(contents); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(a.data, "projects.json"))
}

func (a *app) open(id string) (*project, error) {
	if !validID.MatchString(id) {
		return nil, fmt.Errorf("invalid project ID")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return nil, errClosing
	}
	p := a.projects[id]
	if p == nil {
		return nil, os.ErrNotExist
	}
	if safe, reason := tools.IsPathSafe(p.Dir, filepath.Join(a.data, "projects")); !safe {
		return nil, fmt.Errorf("unsafe project directory: %s", reason)
	}
	if p.session == nil {
		if err := os.MkdirAll(p.Dir, 0700); err != nil {
			return nil, err
		}
		session, err := a.client.NewSession(id, caretz.SessionOptions{
			Adapter: "coderun", WorkspaceDir: p.Dir, PermissionTimeout: 10 * time.Minute,
		})
		if err != nil {
			return nil, err
		}
		p.session = session
		session.SetAssistantDeltaHandler(p.updateLive)
		// Reset provisional text at model-request boundaries and when its durable
		// message arrives. This also keeps reconnects from duplicating final text.
		for _, eventType := range []string{eventing.LLMReasoningDeltaCreated, eventing.LLMAgentMessageCreated, eventing.LLMAgentResponseCreated} {
			if err := session.OnContextEvent(eventType, func(e *types.Event) error {
				if e.EventType == eventing.LLMReasoningDeltaCreated {
					var payload eventing.LLMReasoningDelta
					if json.Unmarshal([]byte(e.Payload), &payload) != nil || !payload.Started {
						return nil
					}
				}
				p.updateLive("", true)
				return nil
			}); err != nil {
				return nil, err
			}
		}
	}
	return p, nil
}

func (p *project) snapshot() turnState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.state
}

func (p *project) updateLive(delta string, clear bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if clear {
		p.liveText = ""
	}
	p.liveText += delta
	p.liveVersion++
}

func (p *project) liveSnapshot() (frame, uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return frame{Kind: "assistant_stream", Text: p.liveText}, p.liveVersion
}

func (a *app) start(p *project, prompt string) error {
	return a.startWork(p, "", func(ctx context.Context, stop <-chan struct{}) (string, *tools.VerificationResult, error) {
		response, err := p.session.CallUntilStopWithPermissions(ctx, prompt, stop)
		text := ""
		if response != nil {
			text = response.Content
		}
		return text, nil, err
	})
}

func (a *app) startCommand(p *project, command string) error {
	return a.startWork(p, command, func(ctx context.Context, _ <-chan struct{}) (string, *tools.VerificationResult, error) {
		result, err := p.session.RunCommandWithPermissions(ctx, command)
		return "", &result, err
	})
}

func (a *app) startWork(p *project, command string, work func(context.Context, <-chan struct{}) (string, *tools.VerificationResult, error)) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closing {
		return errClosing
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.Running {
		return errBusy
	}
	p.stop, p.once = make(chan struct{}), sync.Once{}
	turnCtx, cancel := context.WithCancel(a.ctx)
	p.cancelTurn = cancel
	p.state = turnState{Kind: "status", Running: true, Revision: p.state.Revision + 1, Command: command, StartedAt: time.Now().UTC()}
	p.liveText = ""
	p.liveVersion++
	stop := p.stop
	a.turns.Add(1)
	go func() {
		defer a.turns.Done()
		defer cancel()
		text, result, err := work(turnCtx, stop)
		p.mu.Lock()
		defer p.mu.Unlock()
		p.state.Running = false
		p.liveText = ""
		p.liveVersion++
		p.state.Revision++
		p.state.Kind = "turn_done"
		p.state.Text, p.state.Result = text, result
		if err != nil {
			p.state.Kind, p.state.Text = "turn_error", err.Error()
			if turnCtx.Err() != nil {
				p.state.Kind, p.state.Text = "turn_done", "Stopped."
			}
		}
	}()
	return nil
}

func (p *project) cancel() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state.Running {
		p.once.Do(func() { close(p.stop); p.cancelTurn() })
	}
}

func (a *app) close() {
	a.mu.Lock()
	a.closing = true
	for _, p := range a.projects {
		p.cancel()
	}
	a.mu.Unlock()
	a.turns.Wait()
}
