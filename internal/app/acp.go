package app

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/chatgptauth"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/sandbox"
	"github.com/makkalot/caretz/internal/sessions"
	"github.com/makkalot/caretz/internal/skills"
	"github.com/makkalot/caretz/internal/slashcmd"
	"github.com/makkalot/caretz/internal/tools"
	"github.com/makkalot/caretz/internal/uuid"
	"github.com/makkalot/caretz/lib/eventing"
	"github.com/makkalot/caretz/lib/llm"
	"github.com/makkalot/caretz/lib/types"
)

const acpAdapterType = "acp"

var acpSessionIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

const (
	acpModeBuild          = "build"
	acpModePlan           = "plan"
	acpMaxAttachmentBytes = 20 << 20
)

type acpServer struct {
	rt            *Runtime
	in            io.Reader
	out           io.Writer
	writeMu       sync.Mutex
	notifyMu      sync.Mutex
	sessionsMu    sync.RWMutex
	sessions      map[string]*acpSession
	allowedModels map[string]bool
	// fileConfigMu guards rt.FileConfig, which allowing a model replaces.
	fileConfigMu sync.RWMutex
}

func (s *acpServer) fileConfig() config.Config {
	s.fileConfigMu.RLock()
	defer s.fileConfigMu.RUnlock()
	return s.rt.FileConfig
}

// ACPServer is the internal server surface shared by the caretz ACP command and
// the public in-process host.
type ACPServer interface {
	ServeACP(context.Context, io.Reader, io.Writer) error
}

type acpSession struct {
	modelNotices    []string
	id              string
	cwd             string
	debugLogPath    string
	session         *agent.AgentSession
	slashCommands   *slashcmd.Registry
	activeModelRef  string
	activeReasoning string
	activeMode      string
	mu              sync.Mutex
	active          *acpTurn
	reasoningMu     sync.Mutex
	reasoningDeltas map[string]bool
	reasoningIDs    map[string]string
	subagentSeq     uint64
	statusSeq       uint64
	fileSender      *acpFileSender
}

type acpFileSender struct {
	server    *acpServer
	sessionID string
	session   *agent.AgentSession
}

type acpTurn struct {
	cancel context.CancelFunc
	stop   chan struct{}
	once   sync.Once
}

type acpRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type acpResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *acpError       `json:"error,omitempty"`
}

type acpNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type acpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type acpSessionParams struct {
	SessionID     string `json:"sessionId"`
	CWD           string `json:"cwd"`
	CaretzGateway bool   `json:"_caretzGateway"`
}

type acpPromptParams struct {
	SessionID string            `json:"sessionId"`
	Prompt    []acpContentBlock `json:"prompt"`
	Content   []acpContentBlock `json:"content"`
	Message   string            `json:"message"`
}

type acpSetConfigOptionParams struct {
	SessionID string          `json:"sessionId"`
	ConfigID  string          `json:"configId"`
	Value     json.RawMessage `json:"value"`
}

type acpContentBlock struct {
	Type     string       `json:"type"`
	Text     string       `json:"text"`
	Data     string       `json:"data"`
	MimeType string       `json:"mimeType"`
	URI      string       `json:"uri"`
	Name     string       `json:"name"`
	Resource *acpResource `json:"resource"`
}

type acpResource struct {
	URI      string `json:"uri"`
	Text     string `json:"text"`
	Blob     string `json:"blob"`
	MimeType string `json:"mimeType"`
}

type acpAttachmentSidecar struct {
	ID       string `json:"id"`
	Kind     string `json:"kind"`
	Name     string `json:"name"`
	MimeType string `json:"mimeType"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
	File     string `json:"file"`
}

func RunACP(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if stdin == nil {
		stdin = os.Stdin
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	rt, code := BuildRuntime(ctx, args, stderr)
	if code != 0 {
		return code
	}
	if rt == nil {
		return 0
	}
	return runACPServer(ctx, rt, stdin, stdout, stderr)
}

func runACPServer(ctx context.Context, rt *Runtime, stdin io.Reader, stdout, stderr io.Writer) int {
	return runACPServerWithAllowedModels(ctx, rt, stdin, stdout, stderr, nil)
}

func runACPServerWithAllowedModels(ctx context.Context, rt *Runtime, stdin io.Reader, stdout, stderr io.Writer, allowedModels []string) int {
	server, err := NewACPServer(rt, allowedModels)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return 1
	}
	if err := server.ServeACP(ctx, stdin, stdout); err != nil {
		output.Errorf(stderr, "%v", err)
		return 1
	}
	return 0
}

// NewACPServer constructs an ACP server over an existing configured runtime.
func NewACPServer(rt *Runtime, allowedModels []string) (ACPServer, error) {
	if rt == nil {
		return nil, fmt.Errorf("runtime is nil")
	}
	allowed := make(map[string]bool, len(allowedModels))
	for _, ref := range allowedModels {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		if _, ok := modelChoiceByRef(rt.FileConfig.ModelChoices(), ref); !ok {
			return nil, fmt.Errorf("allowed ACP model %q is not a configured model", ref)
		}
		allowed[ref] = true
	}

	server := &acpServer{
		rt:            rt,
		sessions:      map[string]*acpSession{},
		allowedModels: allowed,
	}
	return server, nil
}

func (s *acpServer) ServeACP(ctx context.Context, input io.Reader, output io.Writer) (err error) {
	if input == nil {
		return fmt.Errorf("ACP input is required")
	}
	if output == nil {
		return fmt.Errorf("ACP output is required")
	}
	s.in = input
	s.out = output
	defer func() { err = errors.Join(err, s.closeSessions()) }()
	return s.Serve(ctx)
}

func (s *acpServer) Serve(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if s.in == nil {
		return fmt.Errorf("ACP input is required")
	}
	if s.out == nil {
		return fmt.Errorf("ACP output is required")
	}
	s.logInfo("ACP server started")
	defer s.logInfo("ACP server stopped")
	scanner := bufio.NewScanner(s.in)
	scanner.Buffer(make([]byte, 0, 64*1024), 32<<20)
	var wg sync.WaitGroup
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			wg.Wait()
			return err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var req acpRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			s.logInfo("ACP request rejected: invalid JSON")
			s.writeResponse(acpResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &acpError{Code: -32700, Message: "parse error"}})
			continue
		}
		s.logDebug("ACP request: method=%s id=%s", req.Method, acpLogRequestID(req.ID))
		if len(req.ID) == 0 {
			_, rpcErr := s.handle(ctx, req)
			s.logRequestResult(req, rpcErr)
			continue
		}
		run := func(req acpRequest) {
			result, rpcErr := s.handle(ctx, req)
			s.logRequestResult(req, rpcErr)
			resp := acpResponse{JSONRPC: "2.0", ID: req.ID, Result: result}
			if rpcErr != nil {
				resp.Result = nil
				resp.Error = rpcErr
			}
			if rpcErr == nil && req.Method == "session/load" {
				s.notifyMu.Lock()
				var p acpSessionParams
				if decodeParams(req.Params, &p) == nil {
					if session := s.getSession(p.SessionID); session != nil {
						if err := s.replaySessionLocked(session); err != nil {
							s.rt.Logger.Warn("Failed to replay ACP session after load: %v", err)
						}
					}
				}
				s.writeResponse(resp)
				s.notifyMu.Unlock()
			} else {
				s.writeResponse(resp)
			}
			if rpcErr == nil {
				s.notifyAfterResponse(req, result)
			}
		}
		if req.Method == "session/prompt" {
			wg.Add(1)
			go func(req acpRequest) {
				defer wg.Done()
				run(req)
			}(req)
			continue
		}
		run(req)
	}
	if err := scanner.Err(); err != nil {
		wg.Wait()
		return err
	}
	wg.Wait()
	return nil
}

func (s *acpServer) closeSessions() error {
	s.sessionsMu.Lock()
	sessions := make([]*acpSession, 0, len(s.sessions))
	for id, session := range s.sessions {
		sessions = append(sessions, session)
		delete(s.sessions, id)
	}
	s.sessionsMu.Unlock()
	var errs []error
	for _, session := range sessions {
		session.mu.Lock()
		turn := session.active
		session.active = nil
		session.mu.Unlock()
		if turn != nil {
			turn.stopNow()
		}
		if session.session != nil {
			if err := session.session.Close(); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func (s *acpServer) logInfo(format string, args ...any) {
	if s != nil && s.rt != nil && s.rt.Logger != nil {
		s.rt.Logger.Info(format, args...)
	}
}

func (s *acpServer) logDebug(format string, args ...any) {
	if s != nil && s.rt != nil && s.rt.Logger != nil {
		s.rt.Logger.Debug(format, args...)
	}
}

func (s *acpServer) logRequestResult(req acpRequest, rpcErr *acpError) {
	if rpcErr != nil {
		s.logInfo("ACP request failed: method=%s id=%s code=%d message=%s", req.Method, acpLogRequestID(req.ID), rpcErr.Code, rpcErr.Message)
		return
	}
	s.logDebug("ACP request complete: method=%s id=%s", req.Method, acpLogRequestID(req.ID))
}

func acpLogRequestID(id json.RawMessage) string {
	if len(id) == 0 {
		return "notification"
	}
	const maxIDBytes = 80
	if len(id) > maxIDBytes {
		return string(id[:maxIDBytes]) + "..."
	}
	return string(id)
}

func (s *acpServer) handle(ctx context.Context, req acpRequest) (any, *acpError) {
	switch req.Method {
	case "initialize":
		return map[string]any{
			"protocolVersion": 1,
			"agentInfo": map[string]any{
				"name":    "caretz",
				"title":   "caretz",
				"version": "dev",
			},
			"agentCapabilities": map[string]any{
				"loadSession": true,
				"promptCapabilities": map[string]any{
					"embeddedContext": true,
					"image":           true,
				},
				"sessionCapabilities": map[string]any{
					"list":   map[string]any{},
					"resume": map[string]any{},
				},
			},
			"authMethods": []any{},
		}, nil
	case "session/new":
		return s.handleSessionNew(req.Params)
	case "session/list":
		return s.handleSessionList(req.Params)
	case "session/load", "session/resume":
		return s.handleSessionLoad(req.Method, req.Params)
	case "session/prompt":
		return s.handlePrompt(ctx, req.Params)
	case "session/cancel":
		return s.handleCancel(req.Params)
	case "session/close":
		return s.handleClose(req.Params)
	case "session/set_model":
		var p struct {
			SessionID string `json:"sessionId"`
			ModelID   string `json:"modelId"`
		}
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, invalidParams(err)
		}
		value, _ := json.Marshal(p.ModelID)
		params, _ := json.Marshal(acpSetConfigOptionParams{SessionID: p.SessionID, ConfigID: "model", Value: value})
		return s.handleSetConfigOption(ctx, params)
	case "session/set_config_option":
		return s.handleSetConfigOption(ctx, req.Params)
	default:
		return nil, &acpError{Code: -32601, Message: "method not found"}
	}
}

func (s *acpServer) handleSessionNew(params json.RawMessage) (any, *acpError) {
	var p acpSessionParams
	if err := decodeParams(params, &p); err != nil {
		return nil, invalidParams(err)
	}
	if strings.TrimSpace(p.CWD) == "" {
		p.CWD = s.rt.CWD
	}
	cwd, err := resolveRootDir(p.CWD)
	if err != nil {
		return nil, invalidParams(fmt.Errorf("invalid cwd: %w", err))
	}
	id := strings.TrimSpace(p.SessionID)
	if id == "" {
		id = uuid.NewV4().String()
	}
	if err := validateACPSessionID(id); err != nil {
		return nil, invalidParams(err)
	}
	if s.getSession(id) != nil {
		return nil, invalidParams(fmt.Errorf("session already exists: %s", id))
	}
	if _, err := os.Stat(filepath.Join(s.rt.SessionsDir, acpAdapterType, id, "events.jsonpl")); err == nil {
		return nil, invalidParams(fmt.Errorf("session already exists: %s", id))
	} else if !os.IsNotExist(err) {
		return nil, internalError(err)
	}
	session, err := s.openSession(id, cwd)
	if err != nil {
		s.writeACPDebugLog(id, "session/new failed: cwd=%s error=%v", cwd, err)
		return nil, internalError(err)
	}
	s.sessionsMu.Lock()
	s.sessions[id] = session
	s.sessionsMu.Unlock()
	session.debugf("session/new ready: cwd=%s", cwd)
	return s.sessionSetupResult(session), nil
}

func (s *acpServer) handleSessionLoad(method string, params json.RawMessage) (any, *acpError) {
	var p acpSessionParams
	if err := decodeParams(params, &p); err != nil {
		return nil, invalidParams(err)
	}
	if err := validateACPSessionID(p.SessionID); err != nil {
		return nil, invalidParams(err)
	}
	cwd := strings.TrimSpace(p.CWD)
	if cwd == "" {
		cwd = s.rt.CWD
	}
	resolvedCWD, err := resolveRootDir(cwd)
	if err != nil {
		return nil, invalidParams(fmt.Errorf("invalid cwd: %w", err))
	}
	if existing := s.getSession(p.SessionID); existing != nil {
		if existing.cwd != resolvedCWD {
			return nil, invalidParams(fmt.Errorf("session belongs to a different workspace"))
		}
		return s.sessionSetupResult(existing), nil
	}
	if _, err := os.Stat(filepath.Join(s.rt.SessionsDir, acpAdapterType, p.SessionID, "events.jsonpl")); err != nil {
		if os.IsNotExist(err) {
			return nil, invalidParams(fmt.Errorf("session not found: %s", p.SessionID))
		}
		return nil, internalError(err)
	}
	state, found, err := loadSessionRuntime(sessionRuntimePath(s.rt.SessionsDir, acpAdapterType, p.SessionID))
	if err != nil {
		return nil, internalError(err)
	}
	if !found || (p.CaretzGateway && state.WorkspaceDir == "") || (state.WorkspaceDir != "" && state.WorkspaceDir != resolvedCWD) {
		return nil, invalidParams(fmt.Errorf("session belongs to a different workspace"))
	}
	if state.WorkspaceDir == "" {
		state.WorkspaceDir = resolvedCWD
		if err := saveSessionRuntime(sessionRuntimePath(s.rt.SessionsDir, acpAdapterType, p.SessionID), state); err != nil {
			return nil, internalError(err)
		}
	}
	session, err := s.openSession(p.SessionID, resolvedCWD)
	if err != nil {
		s.writeACPDebugLog(p.SessionID, "%s failed: cwd=%s error=%v", method, resolvedCWD, err)
		return nil, internalError(err)
	}
	s.sessionsMu.Lock()
	s.sessions[p.SessionID] = session
	s.sessionsMu.Unlock()
	session.debugf("%s ready: cwd=%s", method, resolvedCWD)
	if method == "session/load" {
		return s.sessionSetupResult(session), nil
	}
	return s.sessionSetupResult(session), nil
}

func (s *acpServer) handleSessionList(params json.RawMessage) (any, *acpError) {
	var p acpSessionParams
	if err := decodeParams(params, &p); err != nil {
		return nil, invalidParams(err)
	}
	cwd := strings.TrimSpace(p.CWD)
	if cwd == "" {
		cwd = s.rt.CWD
	}
	resolvedCWD, err := resolveRootDir(cwd)
	if err != nil {
		return nil, invalidParams(fmt.Errorf("invalid cwd: %w", err))
	}
	items, err := sessions.ListWithOptions(s.rt.SessionsDir, sessions.ListOptions{Adapter: acpAdapterType})
	if err != nil {
		return nil, internalError(err)
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		if validateACPSessionID(item.ID) != nil {
			continue
		}
		state, found, err := loadSessionRuntime(sessionRuntimePath(s.rt.SessionsDir, acpAdapterType, item.ID))
		if err != nil || !found || state.WorkspaceDir != resolvedCWD {
			continue
		}
		result = append(result, map[string]any{
			"sessionId": item.ID,
			"title":     item.FirstPrompt,
			"updatedAt": item.LastModified.UTC().Format(time.RFC3339),
		})
	}
	return map[string]any{"sessions": result}, nil
}

func validateACPSessionID(id string) error {
	id = strings.TrimSpace(id)
	if !acpSessionIDPattern.MatchString(id) || id == "." || id == ".." {
		return fmt.Errorf("invalid sessionId")
	}
	return nil
}

func (s *acpServer) notifyAfterResponse(req acpRequest, result any) {
	switch req.Method {
	case "session/new":
		resultMap, _ := result.(map[string]any)
		sessionID, _ := resultMap["sessionId"].(string)
		if session := s.getSession(sessionID); session != nil {
			s.notifyAvailableCommands(session)
			for _, notice := range session.modelNotices {
				s.notifyAgentText(session.id, notice)
			}
		}
	case "session/load", "session/resume":
		var p acpSessionParams
		if err := decodeParams(req.Params, &p); err != nil {
			return
		}
		session := s.getSession(p.SessionID)
		if session == nil {
			return
		}
		s.notifyAvailableCommands(session)
		for _, notice := range session.modelNotices {
			s.notifyAgentText(session.id, notice)
		}
	}
}

func (s *acpServer) handlePrompt(ctx context.Context, params json.RawMessage) (any, *acpError) {
	var p acpPromptParams
	if err := decodeParams(params, &p); err != nil {
		return nil, invalidParams(err)
	}
	if err := validateACPSessionID(p.SessionID); err != nil {
		return nil, invalidParams(err)
	}
	session := s.getSession(p.SessionID)
	if session == nil {
		return nil, invalidParams(fmt.Errorf("unknown sessionId: %s", p.SessionID))
	}
	prompt, displayPrompt, attachments, cleanup, err := s.prepareACPPrompt(session, p)
	if err != nil {
		return nil, invalidParams(err)
	}
	committed := false
	defer func() {
		if !committed {
			cleanup()
		}
	}()
	if strings.TrimSpace(prompt) == "" {
		return nil, invalidParams(fmt.Errorf("prompt is required"))
	}
	session.debugf("session/prompt start: chars=%d blocks=%d content_blocks=%d", len(prompt), len(p.Prompt), len(p.Content))
	session.mu.Lock()
	if session.active != nil {
		session.mu.Unlock()
		session.debugf("session/prompt rejected: active prompt already running")
		return nil, &acpError{Code: -32000, Message: "session already has an active prompt"}
	}
	turnCtx, cancel := context.WithCancel(ctx)
	turn := &acpTurn{cancel: cancel, stop: make(chan struct{})}
	session.active = turn
	session.mu.Unlock()
	defer func() {
		turn.stopNow()
		session.mu.Lock()
		if session.active == turn {
			session.active = nil
		}
		session.mu.Unlock()
	}()

	userEvent := eventing.NewLogMessageWithAttachments(session.id, "", prompt, displayPrompt, acpAdapterType, session.id, "", attachments)
	accepted := func() {
		committed = true
		message, _ := eventing.DecodeLogMessage(userEvent)
		s.notify("session/update", map[string]any{
			"sessionId": session.id,
			"update": map[string]any{
				"sessionUpdate": "user_message_chunk",
				"content":       acpUserContent(session.id, message),
			},
		})
	}
	if session.slashCommands.HandlesSessionInput(session.session, prompt) {
		if err := session.session.AppendAuditEvent(userEvent); err != nil {
			return nil, internalError(err)
		}
		accepted()
	}
	commandName, _, slashCommand := slashcmd.Parse(prompt)
	if slashCommand && (commandName == "exit" || commandName == "quit") {
		return nil, internalError(fmt.Errorf("command /%s is not supported by ACP", commandName))
	}
	if result, handled, err := session.slashCommands.Execute(turnCtx, session.session, prompt); handled {
		if !committed {
			if appendErr := session.session.AppendAuditEvent(userEvent); appendErr != nil {
				return nil, internalError(appendErr)
			}
			accepted()
		}
		if err != nil {
			session.debugf("session/prompt command failed: error=%v", err)
			return nil, internalError(err)
		}
		if result.NewSession != nil {
			oldSession := session.session
			session.session = result.NewSession
			_ = oldSession.Close()
			s.attachACPStreaming(session)
			if err := s.registerSessionEvents(session); err != nil {
				session.debugf("session/prompt command event registration failed: error=%v", err)
				return nil, internalError(err)
			}
		}
		if result.ActiveModelRef != "" {
			session.mu.Lock()
			session.activeModelRef = result.ActiveModelRef
			if result.ActiveReasoningSet {
				session.activeReasoning = result.ActiveReasoningEffort
			}
			session.mu.Unlock()
			s.notifyConfigOptions(session)
		}
		if result.Message != "" && !result.MessagePublished {
			s.notifyAgentText(session.id, result.Message)
		}
		session.debugf("session/prompt command complete: stopReason=end_turn")
		return map[string]any{"stopReason": "end_turn"}, nil
	}
	if committed {
		_, err = session.session.ProcessUserEventUntilStop(turnCtx, userEvent, turn.stop)
	} else {
		_, err = session.session.CallUserEvent(turnCtx, userEvent, accepted)
	}
	if err != nil {
		if agent.IsCleanStop(err) {
			s.notifyProvisionalClear(session.id)
			session.debugf("session/prompt cancelled")
			return map[string]any{"stopReason": "cancelled"}, nil
		}
		s.notifyProvisionalClear(session.id)
		session.debugf("session/prompt failed: error=%v", err)
		return nil, internalError(err)
	}
	session.debugf("session/prompt complete: stopReason=end_turn")
	return map[string]any{"stopReason": "end_turn"}, nil
}

func (s *acpServer) handleCancel(params json.RawMessage) (any, *acpError) {
	var p acpSessionParams
	if err := decodeParams(params, &p); err != nil {
		return nil, invalidParams(err)
	}
	if err := validateACPSessionID(p.SessionID); err != nil {
		return nil, invalidParams(err)
	}
	session := s.getSession(p.SessionID)
	if session == nil {
		return nil, invalidParams(fmt.Errorf("unknown sessionId: %s", p.SessionID))
	}
	session.mu.Lock()
	turn := session.active
	session.mu.Unlock()
	if turn != nil {
		turn.stopNow()
		session.debugf("session/cancel requested: active=true")
	} else {
		session.debugf("session/cancel requested: active=false")
	}
	return map[string]any{}, nil
}

func (s *acpServer) handleClose(params json.RawMessage) (any, *acpError) {
	var p acpSessionParams
	if err := decodeParams(params, &p); err != nil {
		return nil, invalidParams(err)
	}
	if err := validateACPSessionID(p.SessionID); err != nil {
		return nil, invalidParams(err)
	}
	session := s.getSession(p.SessionID)
	if session == nil {
		return nil, invalidParams(fmt.Errorf("unknown sessionId: %s", p.SessionID))
	}
	session.mu.Lock()
	turn := session.active
	session.active = nil
	session.mu.Unlock()
	if turn != nil {
		turn.stopNow()
	}
	session.debugf("session/close requested: active=%v", turn != nil)
	s.sessionsMu.Lock()
	delete(s.sessions, p.SessionID)
	s.sessionsMu.Unlock()
	if err := session.session.Close(); err != nil {
		return nil, internalError(err)
	}
	return map[string]any{}, nil
}

func (s *acpServer) handleSetConfigOption(ctx context.Context, params json.RawMessage) (any, *acpError) {
	var p acpSetConfigOptionParams
	if err := decodeParams(params, &p); err != nil {
		return nil, invalidParams(err)
	}
	if err := validateACPSessionID(p.SessionID); err != nil {
		return nil, invalidParams(err)
	}
	session := s.getSession(p.SessionID)
	if session == nil {
		return nil, invalidParams(fmt.Errorf("unknown sessionId: %s", p.SessionID))
	}
	switch p.ConfigID {
	case "model":
		var ref string
		if err := json.Unmarshal(p.Value, &ref); err != nil || strings.TrimSpace(ref) == "" {
			return nil, invalidParams(fmt.Errorf("model value is required"))
		}
		result, err := s.switchACPModel(ctx, session, strings.TrimSpace(ref), "")
		if err != nil {
			return nil, internalError(err)
		}
		s.notifyAgentText(session.id, "model switched to "+result.Ref)
		session.mu.Lock()
		session.activeModelRef = result.Ref
		session.activeReasoning = result.ReasoningEffort
		session.mu.Unlock()
	case "reasoning_effort":
		var reasoning string
		if err := json.Unmarshal(p.Value, &reasoning); err != nil || strings.TrimSpace(reasoning) == "" {
			return nil, invalidParams(fmt.Errorf("reasoning_effort value is required"))
		}
		session.mu.Lock()
		ref := session.activeModelRef
		session.mu.Unlock()
		if !s.isValidACPReasoning(ref, strings.TrimSpace(reasoning)) {
			return nil, invalidParams(fmt.Errorf("invalid reasoning_effort %q for model %s", reasoning, ref))
		}
		result, err := s.switchACPModel(ctx, session, ref, strings.TrimSpace(reasoning))
		if err != nil {
			return nil, internalError(err)
		}
		session.mu.Lock()
		session.activeReasoning = result.ReasoningEffort
		session.mu.Unlock()
	case "mode":
		var mode string
		if err := json.Unmarshal(p.Value, &mode); err != nil || strings.TrimSpace(mode) == "" {
			return nil, invalidParams(fmt.Errorf("mode value is required"))
		}
		if err := s.switchACPMode(session, strings.TrimSpace(mode)); err != nil {
			return nil, invalidParams(err)
		}
	default:
		return nil, invalidParams(fmt.Errorf("unknown configId: %s", p.ConfigID))
	}
	return map[string]any{"configOptions": s.configOptionsForSession(session)}, nil
}

func (s *acpServer) sessionSetupResult(session *acpSession) map[string]any {
	result := map[string]any{"sessionId": session.id}
	if options := s.configOptionsForSession(session); len(options) > 0 {
		result["configOptions"] = options
	}
	return result
}

func (s *acpServer) configOptionsForSession(session *acpSession) []map[string]any {
	choices := s.modelChoicesForSession(session)
	current := s.currentACPModelRef()
	if session != nil {
		session.mu.Lock()
		if session.activeModelRef != "" {
			current = session.activeModelRef
		}
		session.mu.Unlock()
	}
	if len(choices) > 0 && !modelChoiceExists(choices, current) {
		current = choices[0].Ref
	}
	currentChoice, _ := modelChoiceByRef(choices, current)
	currentReasoning := s.currentACPReasoningForRef(current)
	currentMode := s.currentACPMode()
	options := make([]map[string]any, 0, len(choices))
	for _, choice := range choices {
		option := map[string]any{
			"value": choice.Ref,
			"name":  acpModelChoiceName(choice),
		}
		if description := acpModelChoiceDescription(choice); description != "" {
			option["description"] = description
		}
		options = append(options, option)
	}
	if session != nil {
		session.mu.Lock()
		if session.activeReasoning != "" {
			currentReasoning = session.activeReasoning
		}
		if session.activeMode != "" {
			currentMode = session.activeMode
		}
		session.mu.Unlock()
	}
	var configOptions []map[string]any
	if len(choices) > 0 {
		configOptions = append(configOptions, map[string]any{
			"id":           "model",
			"name":         "Model",
			"description":  "The LLM model to use for this session",
			"category":     "model",
			"type":         "select",
			"currentValue": current,
			"options":      options,
		})
	}
	if reasoningValues := acpReasoningValues(currentChoice); len(reasoningValues) > 0 {
		configOptions = append(configOptions, map[string]any{
			"id":           "reasoning_effort",
			"name":         "Reasoning",
			"description":  "The reasoning effort to use for this session",
			"category":     "thought_level",
			"type":         "select",
			"currentValue": acpReasoningCurrent(currentReasoning, reasoningValues),
			"options":      acpReasoningOptions(reasoningValues),
		})
	}
	configOptions = append(configOptions, map[string]any{
		"id":           "mode",
		"name":         "Profile",
		"description":  "Controls whether the session can modify files",
		"category":     "mode",
		"type":         "select",
		"currentValue": currentMode,
		"options": []map[string]any{
			{"value": acpModeBuild, "name": "Build", "description": "Write and modify code with normal tool access"},
			{"value": acpModePlan, "name": "Plan", "description": "Inspect and propose changes without persistent workspace edits"},
		},
	})
	return configOptions
}

func (s *acpServer) notifyConfigOptions(session *acpSession) {
	options := s.configOptionsForSession(session)
	if len(options) == 0 {
		return
	}
	s.notify("session/update", map[string]any{
		"sessionId": session.id,
		"update": map[string]any{
			"sessionUpdate": "config_option_update",
			"configOptions": options,
		},
	})
}

func (s *acpServer) currentACPModelRef() string {
	if s.rt.Settings.ActiveModelRef != "" {
		return s.rt.Settings.ActiveModelRef
	}
	return s.fileConfig().ActiveModel
}

func (s *acpServer) currentACPReasoningForRef(ref string) string {
	if ref == s.currentACPModelRef() {
		if reasoning := strings.TrimSpace(s.rt.Settings.ReasoningEffort); reasoning != "" {
			return reasoning
		}
	}
	choices := s.modelChoices()
	choice, ok := modelChoiceByRef(choices, ref)
	if ok && strings.TrimSpace(choice.Reasoning) != "" {
		return strings.TrimSpace(choice.Reasoning)
	}
	return ""
}

func (s *acpServer) isValidACPReasoning(ref, reasoning string) bool {
	choice, ok := modelChoiceByRef(s.modelChoices(), ref)
	if !ok {
		return false
	}
	for _, value := range acpReasoningValues(choice) {
		if value == strings.TrimSpace(reasoning) {
			return true
		}
	}
	return false
}

func acpReasoningValues(choice config.ModelChoice) []string {
	if strings.TrimSpace(choice.Reasoning) == "" {
		return nil
	}
	values := normalizeReasoningValues(choice.ReasoningValues)
	if len(values) == 0 {
		values = defaultReasoningShortcutCycle
	}
	if !acpStringSliceContains(values, choice.Reasoning) {
		values = append([]string{choice.Reasoning}, values...)
	}
	return values
}

var defaultReasoningShortcutCycle = []string{"low", "medium", "high", "xhigh"}

func normalizeReasoningValues(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func acpReasoningCurrent(current string, values []string) string {
	current = strings.TrimSpace(current)
	if acpStringSliceContains(values, current) {
		return current
	}
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func acpReasoningOptions(values []string) []map[string]any {
	options := make([]map[string]any, 0, len(values))
	for _, value := range values {
		options = append(options, map[string]any{"value": value, "name": value})
	}
	return options
}

func acpStringSliceContains(values []string, target string) bool {
	target = strings.TrimSpace(target)
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func modelChoiceExists(choices []config.ModelChoice, ref string) bool {
	_, ok := modelChoiceByRef(choices, ref)
	return ok
}

func modelChoiceByRef(choices []config.ModelChoice, ref string) (config.ModelChoice, bool) {
	for _, choice := range choices {
		if choice.Ref == ref {
			return choice, true
		}
	}
	return config.ModelChoice{}, false
}

func acpModelChoiceName(choice config.ModelChoice) string {
	if choice.ModelName != "" {
		return choice.ModelName
	}
	return choice.Ref
}

func acpModelChoiceDescription(choice config.ModelChoice) string {
	var parts []string
	if choice.ProviderName != "" {
		parts = append(parts, choice.ProviderName)
	}
	if choice.Model != "" && choice.Model != choice.ModelKey && choice.Model != choice.ModelName {
		parts = append(parts, choice.Model)
	}
	return strings.Join(parts, " / ")
}

func (s *acpServer) getSession(id string) *acpSession {
	s.sessionsMu.RLock()
	defer s.sessionsMu.RUnlock()
	return s.sessions[id]
}

func (s *acpServer) openSession(id, cwd string) (*acpSession, error) {
	debugLogPath := ""
	if s.acpDebugEnabled() {
		debugLogPath = filepath.Join(s.rt.SessionsDir, acpAdapterType, id, "debug.log")
		s.writeACPDebugLog(id, "openSession start: cwd=%s", cwd)
	}
	skillDirs, agentsDirs := s.resourceDirsForWorkspace(cwd)
	ensureAgentsDirs(agentsDirs, s.rt.Logger)
	readableRoots := runtimeReadableResourceRoots(skillDirs, agentsDirs)
	readableRoots = append(readableRoots, acpAttachmentsDir(s.rt.SessionsDir, id))
	fileSender := &acpFileSender{server: s, sessionID: id}
	registry := s.acpToolRegistryForMode(cwd, s.currentACPMode(), readableRoots...).WithFileSender(fileSender)
	systemPrompt, err := s.systemPromptForWorkspace(cwd, skillDirs, registry)
	if err != nil {
		s.writeACPDebugLog(id, "openSession system prompt failed: error=%v", err)
		return nil, err
	}
	fileConfig := s.fileConfig()
	policy, err := fileConfig.PolicyForWorkspace(cwd)
	if err != nil {
		return nil, err
	}
	// switches are recorded in session history; notices are also shown.
	switches := []string{}
	runtimeSettings := s.rt.Settings
	if s.rt.RequestedSettings != nil {
		runtimeSettings = *s.rt.RequestedSettings
	}
	runtimePath := sessionRuntimePath(s.rt.SessionsDir, acpAdapterType, id)
	restoredSession := false
	if !s.rt.Incognito {
		state, found, err := loadSessionRuntime(runtimePath)
		if err != nil {
			return nil, err
		}
		if found {
			if runtimeSettings, err = restoreRestrictedSession(fileConfig, policy, runtimeSettings, state, s.allowedModels); err != nil {
				return nil, err
			}
			restoredSession = true
		}
	}
	runtimeSettings, notice, err := enforceSessionModel(fileConfig, policy, runtimeSettings, s.allowedModels, s.rt.ExplicitModelSource)
	if err != nil {
		return nil, err
	}
	if err := checkModelEndpoint(fileConfig, policy, runtimeSettings, s.rt.EndpointOverride, s.rt.EndpointOverrideSource); err != nil {
		return nil, err
	}
	if notice != "" {
		switches = append(switches, notice)
	}
	s.logInfo("%s", modelSummary(runtimeSettings, modelSource(s.rt.ExplicitModelSource, restoredSession, runtimeSettings)))
	s.logInfo("session %s adapter=%s workspace=%s", id, acpAdapterType, cwd)
	notices := switches
	if rule := modelRuleNotice(policy); rule != "" {
		notices = append([]string{rule}, switches...)
	}
	// ACP cannot prompt, so an uncovered cloud workspace is shown, not recorded.
	if notice := hostedModelRuleNotice(fileConfig, policy, runtimeSettings.ActiveModelRef, runtimeSettings.BaseURL); notice != "" {
		notices = append(notices, notice)
	}
	if needsCloudConsent(fileConfig, policy, runtimeSettings.ActiveModelRef, runtimeSettings.BaseURL) {
		notices = append([]string{cloudWorkspaceWarning(cwd, runtimeSettings.ActiveModelRef, runtimeSettings.BaseURL)}, notices...)
	}

	provider := s.rt.Provider
	if runtimeSettings.Model != s.rt.Settings.Model || runtimeSettings.ActiveModelRef != s.rt.Settings.ActiveModelRef || runtimeSettings.ReasoningEffort != s.rt.Settings.ReasoningEffort || runtimeSettings.BaseURL != s.rt.Settings.BaseURL || runtimeSettings.APIKey != s.rt.Settings.APIKey || runtimeSettings.Protocol != s.rt.Settings.Protocol {
		provider = chatgptauth.NewProvider(runtimeSettings.APIKey, runtimeSettings.BaseURL, runtimeSettings.Model, llm.OpenAIProviderOptions{
			RequestTimeout:        runtimeSettings.LLMTimeout,
			DisableRequestTimeout: runtimeSettings.LLMTimeout == 0,
			MaxAttempts:           runtimeSettings.LLMMaxAttempts,
			ReasoningEffort:       runtimeSettings.ReasoningEffort,
			Protocol:              runtimeSettings.Protocol,
			Logf:                  s.rt.Logger.Info,
		})
	}
	session, err := agent.NewAgentSession(acpAdapterType, id, agent.SessionParams{
		BaseDir:           s.rt.SessionsDir,
		WorkspaceDir:      cwd,
		Logger:            s.rt.Logger,
		Provider:          provider,
		ToolRegistry:      registry,
		SystemPrompt:      systemPrompt,
		ContextWindow:     runtimeSettings.ContextWindow,
		ReservedTokens:    runtimeSettings.ReservedTokens,
		KeepRecentTokens:  runtimeSettings.KeepRecentTokens,
		SummaryTokens:     runtimeSettings.SummaryTokens,
		DisableTasks:      s.rt.DisableTasks,
		PermissionTimeout: s.rt.PermissionTimeout,
	})
	if err != nil {
		s.writeACPDebugLog(id, "openSession agent session failed: error=%v", err)
		return nil, err
	}
	fileSender.session = session
	if !s.rt.Incognito {
		state := sessionRuntimeStateFromSettings(runtimeSettings, s.fileConfig().Profile)
		state.WorkspaceDir = cwd
		if err := saveSessionRuntime(runtimePath, state); err != nil {
			_ = session.Close()
			return nil, err
		}
	}
	for _, notice := range switches {
		if err := recordModelNotice(session, notice); err != nil {
			_ = session.Close()
			return nil, err
		}
	}
	session.SetAgentsDirs(agentsDirs)
	session.SetSubagentModelResolver(subagentModelResolver{
		fileConfig: fileConfig,
		// Read the live config so rules added by /allow-model apply.
		policyFor: func(workspace string) (config.ModelPolicy, error) {
			return s.fileConfig().PolicyForWorkspace(workspace)
		},
		llmTimeout:     s.rt.Settings.LLMTimeout,
		llmMaxAttempts: s.rt.Settings.LLMMaxAttempts,
		logger:         s.rt.Logger,
		allowedModels:  s.allowedModels,
	})
	acp := &acpSession{
		id:              id,
		modelNotices:    notices,
		cwd:             cwd,
		debugLogPath:    debugLogPath,
		session:         session,
		activeModelRef:  runtimeSettings.ActiveModelRef,
		activeReasoning: runtimeSettings.ReasoningEffort,
		activeMode:      s.currentACPMode(),
		reasoningDeltas: map[string]bool{},
		reasoningIDs:    map[string]string{},
		fileSender:      fileSender,
	}
	slashCommandRegistry, err := s.newACPSlashCommandRegistry(acp, agentsDirs)
	if err != nil {
		s.writeACPDebugLog(id, "openSession command registry failed: error=%v", err)
		return nil, err
	}
	acp.slashCommands = slashCommandRegistry
	if err := s.registerSessionEvents(acp); err != nil {
		acp.debugf("openSession event registration failed: error=%v", err)
		return nil, err
	}
	s.attachACPStreaming(acp)
	acp.debugf("openSession complete: mode=%s model=%s reasoning=%s", acp.activeMode, acp.activeModelRef, acp.activeReasoning)
	return acp, nil
}

func (s *acpServer) attachACPStreaming(acp *acpSession) {
	acp.session.SetAssistantDeltaHandler(func(delta string, clear bool) {
		if clear {
			s.notifyProvisionalClear(acp.id)
			return
		}
		if delta != "" {
			s.notifyProvisionalDelta(acp.id, delta)
		}
	})
}

func (s *acpServer) resourceDirsForWorkspace(cwd string) ([]string, []string) {
	skillDirs := skillDirsForRuntime(s.rt.HomeSkillsDir, cwd, s.rt.ConfigRoot, s.rt.ConfigRootSupplied)
	agentDirs := agentDirsForRuntime(s.rt.HomeAgentsDir, cwd, s.rt.ConfigRoot, s.rt.ConfigRootSupplied)
	return skillDirs, agentDirs
}

func (s *acpServer) systemPromptForWorkspace(cwd string, skillDirs []string, registry *tools.Registry) (string, error) {
	discoveredSkills, err := skills.DiscoverSkills(skillDirs, s.rt.Logger)
	if err != nil {
		return "", fmt.Errorf("skill discovery failed: %w", err)
	}
	discoveredSkills = filterSkills(discoveredSkills, s.rt.Profile.Skills)
	if len(discoveredSkills) > 0 {
		s.rt.Logger.Info("skills %s (ACP workspace %s)", skillNames(discoveredSkills), cwd)
		for _, skill := range discoveredSkills {
			s.rt.Logger.Debug("  Skill: %s - %s (%s)", skill.Name, skill.Description, skill.Location)
		}
	}
	agentInstructions, err := skills.LoadAgentInstructions(cwd)
	if err != nil {
		s.rt.Logger.Warn("AGENTS.md could not be loaded for ACP workspace %s: %v", cwd, err)
	}
	return skills.BuildSystemPromptWithOptions(skills.SystemPromptOptions{
		BasePrompt:        s.rt.SystemPromptBase,
		Skills:            discoveredSkills,
		CWD:               cwd,
		AgentInstructions: agentInstructions,
		Tools:             registry.ToolDefinitions(),
	}), nil
}

func (s *acpServer) currentACPMode() string {
	if s.rt.PlanMode {
		return acpModePlan
	}
	return acpModeBuild
}

func (s *acpServer) acpToolRegistryForMode(cwd, mode string, readableRoots ...string) *tools.Registry {
	registry := s.rt.ToolRegistry.WithWorkspaceDir(cwd).WithReadableRoots(readableRoots...).WithPlanMode(mode == acpModePlan)
	settings := s.rt.SandboxSettings
	sandboxConfig := sandbox.Config{
		UnrestrictedNetwork: settings.UnrestrictedNetwork,
		AllowLoopback:       settings.AllowLoopback,
		NetworkAllowlist:    settings.NetworkAllowlist,
		ReadOnly:            mode == acpModePlan,
		ReadableRoots:       settings.ReadableRoots,
		WritableRoots:       settings.WritableRoots,
		AllowDocker:         settings.AllowDocker,
		AllowSystemBus:      settings.AllowSystemBus,
		AllowGPU:            settings.AllowGPU,
		HiddenPaths:         settings.HiddenPaths,
		SessionPaths:        []string{s.rt.SessionsDir},
		Environment:         settings.Environment,
	}
	if rgPath, err := exec.LookPath("rg"); err == nil {
		if resolved, err := filepath.EvalSymlinks(rgPath); err == nil {
			sandboxConfig.ExtraBinaries = append(sandboxConfig.ExtraBinaries, resolved)
		}
	}
	return registry.WithSandboxConfig(sandboxConfig)
}

func (s *acpServer) switchACPMode(session *acpSession, mode string) error {
	switch mode {
	case acpModeBuild, acpModePlan:
	default:
		return fmt.Errorf("invalid mode %q", mode)
	}
	skillDirs, agentsDirs := s.resourceDirsForWorkspace(session.cwd)
	readableRoots := runtimeReadableResourceRoots(skillDirs, agentsDirs)
	readableRoots = append(readableRoots, acpAttachmentsDir(s.rt.SessionsDir, session.id))
	registry := s.acpToolRegistryForMode(session.cwd, mode, readableRoots...)
	if session.fileSender != nil {
		registry = registry.WithFileSender(session.fileSender)
	}
	systemPrompt, err := s.systemPromptForWorkspace(session.cwd, skillDirs, registry)
	if err != nil {
		return err
	}
	if err := session.session.SwitchSessionRuntime(registry, systemPrompt); err != nil {
		return err
	}
	slashCommandRegistry, err := s.newACPSlashCommandRegistry(session, agentsDirs)
	if err != nil {
		return err
	}
	session.mu.Lock()
	session.activeMode = mode
	session.slashCommands = slashCommandRegistry
	session.mu.Unlock()
	return nil
}

func (s *acpServer) newACPSlashCommandRegistry(acp *acpSession, agentsDirs []string) (*slashcmd.Registry, error) {
	modelSwitcher := func(ctx context.Context, session *agent.AgentSession, ref, reasoningEffort string) (slashcmd.ModelSwitchResult, error) {
		return s.switchACPAgentSessionModel(ctx, session, ref, reasoningEffort)
	}
	return newSlashCommandRegistry(acp.cwd, agentsDirs, s.rt.Profile.Agents, s.fileConfig().Profile, slashcmd.ModelsCommand{
		ChoicesFunc: func() []config.ModelChoice { return s.modelChoicesForWorkspace(acp.cwd) },
		CurrentRef: func(session *agent.AgentSession) string {
			acp.mu.Lock()
			ref := acp.activeModelRef
			acp.mu.Unlock()
			if ref != "" {
				return ref
			}
			return session.Model()
		},
		Switcher: modelSwitcher,
		AllowModel: func(_ context.Context, session *agent.AgentSession, ref string) (string, error) {
			return s.allowACPModel(session, ref)
		},
	})
}

// allowACPModel saves a rule allowing ref in the session's workspace and
// applies it to the live config, which every ACP session reads.
func (s *acpServer) allowACPModel(session *agent.AgentSession, ref string) (string, error) {
	ruleFile := s.rt.HomeConfigPath
	if s.rt.ConfigRootSupplied {
		var err error
		if ruleFile, err = config.ModelRulesFile(s.rt.HomeConfigPath, s.rt.ProjectConfigPath); err != nil {
			return "", err
		}
	}
	s.fileConfigMu.Lock()
	defer s.fileConfigMu.Unlock()
	policy, err := s.rt.FileConfig.PolicyForWorkspace(session.WorkspaceDir())
	if err != nil {
		return "", err
	}
	rule, message, err := allowModelCommand(s.rt.FileConfig, &policy, ruleFile, ref)
	if err != nil || rule == nil {
		return message, err
	}
	if s.rt.FileConfig, err = s.rt.FileConfig.WithModelRule(*rule); err != nil {
		return "", err
	}
	return message, recordModelNotice(session, message)
}

func (s *acpServer) switchACPModel(ctx context.Context, session *acpSession, ref, reasoningEffort string) (slashcmd.ModelSwitchResult, error) {
	if session == nil {
		return slashcmd.ModelSwitchResult{}, fmt.Errorf("agent session is required")
	}
	return s.switchACPAgentSessionModel(ctx, session.session, ref, reasoningEffort)
}

func (s *acpServer) switchACPAgentSessionModel(ctx context.Context, session *agent.AgentSession, ref, reasoningEffort string) (slashcmd.ModelSwitchResult, error) {
	if len(s.allowedModels) > 0 && !s.allowedModels[ref] {
		return slashcmd.ModelSwitchResult{}, fmt.Errorf("requested model %q not allowed by adapter allowed-model restriction; refused; current model kept", ref)
	}
	if session == nil {
		return slashcmd.ModelSwitchResult{}, fmt.Errorf("agent session is required")
	}
	policy, err := s.fileConfig().PolicyForWorkspace(session.WorkspaceDir())
	if err != nil {
		return slashcmd.ModelSwitchResult{}, err
	}
	if err := policy.Check(s.fileConfig(), ref); err != nil {
		return slashcmd.ModelSwitchResult{}, err
	}
	switched := s.fileConfig()
	resolved, ok, err := switched.ResolveModelRef(ref)
	if err != nil {
		return slashcmd.ModelSwitchResult{}, fmt.Errorf("failed to resolve model %q: %w", ref, err)
	}
	if !ok {
		return slashcmd.ModelSwitchResult{}, fmt.Errorf("unknown configured model %q", ref)
	}
	settings, err := runtimeSettingsForModelSwitch(resolved)
	if err != nil {
		return slashcmd.ModelSwitchResult{}, err
	}
	if strings.TrimSpace(reasoningEffort) != "" {
		settings.ReasoningEffort = strings.TrimSpace(reasoningEffort)
	}
	warn := func(message string) { s.notifyAgentText(session.ChannelID(), message) }
	// ACP has no cloud prompt, so this only warns and no rule file is written.
	if err := confirmCloudModelSwitch(ctx, session, switched, &policy, s.rt.HomeConfigPath, session.WorkspaceDir(), ref, settings.BaseURL, warn); err != nil {
		return slashcmd.ModelSwitchResult{}, err
	}
	provider := chatgptauth.NewProvider(settings.APIKey, settings.BaseURL, settings.Model, llm.OpenAIProviderOptions{
		RequestTimeout:        s.rt.Settings.LLMTimeout,
		DisableRequestTimeout: s.rt.Settings.LLMTimeout == 0,
		MaxAttempts:           s.rt.Settings.LLMMaxAttempts,
		ReasoningEffort:       settings.ReasoningEffort,
		Protocol:              settings.Protocol,
		Logf:                  s.rt.Logger.Info,
	})
	if err := session.SwitchModel(provider, settings.ContextWindow, settings.ReservedTokens, settings.KeepRecentTokens, settings.SummaryTokens); err != nil {
		return slashcmd.ModelSwitchResult{}, err
	}
	if err := recordModelNotice(session, fmt.Sprintf("model switched to %s", ref)); err != nil {
		return slashcmd.ModelSwitchResult{}, err
	}
	if !s.rt.Incognito {
		state := sessionRuntimeState{ModelRef: ref, Model: settings.Model, ReasoningEffort: settings.ReasoningEffort, WorkspaceDir: session.WorkspaceDir(), Profile: switched.Profile}
		path := sessionRuntimePath(session.BaseDir(), session.AdapterType(), session.ChannelID())
		if err := saveSessionRuntime(path, state); err != nil {
			return slashcmd.ModelSwitchResult{}, err
		}
	}
	if err := config.SaveProjectActiveModel(s.rt.ProjectConfigPath, ref); err != nil {
		return slashcmd.ModelSwitchResult{}, fmt.Errorf("failed to save active model %q: %w", ref, err)
	}
	return slashcmd.ModelSwitchResult{Ref: ref, Model: settings.Model, ReasoningEffort: settings.ReasoningEffort}, nil
}

func (s *acpServer) modelChoices() []config.ModelChoice {
	return s.choicesForPolicy(config.ModelPolicy{})
}
func (s *acpServer) modelChoicesForWorkspace(cwd string) []config.ModelChoice {
	policy, err := s.fileConfig().PolicyForWorkspace(cwd)
	if err != nil {
		return nil
	}
	return s.choicesForPolicy(policy)
}
func (s *acpServer) choicesForPolicy(policy config.ModelPolicy) []config.ModelChoice {
	cfg := s.fileConfig()
	return policy.Filter(cfg, cfg.ModelChoices(), s.allowedModels)
}
func (s *acpServer) modelChoicesForSession(session *acpSession) []config.ModelChoice {
	if session == nil {
		return s.modelChoices()
	}
	return s.modelChoicesForWorkspace(session.cwd)
}
func (s *acpServer) registerSessionEvents(session *acpSession) error {
	for _, eventType := range []string{
		eventing.FileSent,
		eventing.SubagentInvocationStarted,
		eventing.SubagentInvocationProgress,
		eventing.SubagentInvocationCompleted,
		eventing.SubagentInvocationFailed,
		eventing.StatusUpdated,
	} {
		if err := session.session.OnAuditEvent(eventType, func(e *types.Event) error {
			if e.EventType == eventing.StatusUpdated {
				var p eventing.Status
				if err := json.Unmarshal([]byte(e.Payload), &p); err == nil {
					s.notifyStatus(session, p)
				}
				return nil
			}
			if e.EventType == eventing.FileSent {
				file, err := eventing.DecodeSentFile(e)
				if err == nil {
					s.notifySentFile(session.id, file)
				}
				return nil
			}
			var p eventing.SubagentInvocation
			if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
				return nil
			}
			s.notifySubagentInvocation(session, e.EventType, e.Originator.Version, p)
			return nil
		}); err != nil {
			return err
		}
	}
	if err := session.session.OnContextEvent(eventing.LLMReasoningDeltaCreated, func(e *types.Event) error {
		var p eventing.LLMReasoningDelta
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		s.notifyAgentThought(session, p)
		return nil
	}); err != nil {
		return err
	}
	if err := session.session.OnContextEvent(eventing.LLMAgentMessageCreated, func(e *types.Event) error {
		var p eventing.LLMAgentMessage
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		s.notifyReasoningSummary(session, p.ParentOriginatorID, p.ParentOriginatorVersion, p.ReasoningSummary)
		if len(p.Message.ToolCalls) > 0 {
			s.notifyProvisionalClear(session.id)
		}
		for _, tc := range p.Message.ToolCalls {
			s.notifyToolCall(session.id, tc)
		}
		return nil
	}); err != nil {
		return err
	}
	if err := session.session.OnContextEvent(eventing.LLMToolCallResultCreated, func(e *types.Event) error {
		var p eventing.LLMToolCallResult
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		s.notifyToolResult(session.id, p)
		return nil
	}); err != nil {
		return err
	}
	return session.session.OnContextEvent(eventing.LLMAgentResponseCreated, func(e *types.Event) error {
		var p eventing.LLMAgentResponse
		if err := json.Unmarshal([]byte(e.Payload), &p); err != nil {
			return nil
		}
		s.notifyReasoningSummary(session, p.ParentOriginatorID, p.ParentOriginatorVersion, p.ReasoningSummary)
		if p.Message.Content != "" {
			s.notifyAgentText(session.id, p.Message.Content)
		}
		if p.Usage.TotalTokens > 0 {
			s.notify("session/update", map[string]any{
				"sessionId": session.id,
				"update": map[string]any{
					"sessionUpdate": "usage_update",
					"used":          session.session.EstimatedContextTokens(),
					"size":          session.session.ContextWindow(),
				},
			})
		}
		return nil
	})
}

func (s *acpServer) replaySessionLocked(session *acpSession) error {
	audit, err := session.session.AuditEvents()
	if err != nil {
		return err
	}
	contextEvents, err := session.session.GetContextEvents()
	if err != nil {
		return err
	}
	events := append(append([]*types.Event{}, audit...), contextEvents...)
	sort.SliceStable(events, func(i, j int) bool { return events[i].OccurredOn.Before(events[j].OccurredOn) })
	reasoningDelta := map[string]bool{}
	for _, event := range events {
		switch event.EventType {
		case eventing.LogMessageUserCreated, eventing.LogMessageAgentCreated, eventing.LogMessageAgentInterrupted:
			message, decodeErr := eventing.DecodeLogMessage(event)
			if decodeErr != nil || message.Content == "" {
				continue
			}
			kind := "agent_message_chunk"
			if event.EventType == eventing.LogMessageUserCreated {
				kind = "user_message_chunk"
			}
			content := acpTextContent(message.Content)
			if event.EventType == eventing.LogMessageUserCreated {
				content = acpUserContent(session.id, message)
			}
			s.notifyLocked("session/update", map[string]any{"sessionId": session.id, "update": map[string]any{"sessionUpdate": kind, "content": content}})
			if event.EventType == eventing.LogMessageAgentInterrupted {
				recovery, _ := eventing.DecodeRecoveryPayload(event)
				for _, id := range recovery.PendingToolCallIDs {
					s.notifyLocked("session/update", acpToolInterruptedUpdate(session.id, id))
				}
			}
		case eventing.FileSent:
			file, decodeErr := eventing.DecodeSentFile(event)
			if decodeErr == nil {
				s.notifyLocked("session/update", acpSentFileUpdate(session.id, file))
			}
		case eventing.LLMReasoningDeltaCreated:
			var payload eventing.LLMReasoningDelta
			if json.Unmarshal([]byte(event.Payload), &payload) != nil {
				continue
			}
			text := payload.Delta
			if text == "" {
				text = payload.Text
			}
			if text == "" {
				continue
			}
			key := acpReasoningKey(payload.ParentOriginatorID, payload.ParentOriginatorVersion)
			if payload.Delta != "" {
				reasoningDelta[key] = true
			}
			s.notifyLocked("session/update", acpThoughtUpdate(session.id, session.reasoningMessageID(key), text))
		case eventing.LLMAgentMessageCreated:
			var payload eventing.LLMAgentMessage
			if json.Unmarshal([]byte(event.Payload), &payload) != nil {
				continue
			}
			key := acpReasoningKey(payload.ParentOriginatorID, payload.ParentOriginatorVersion)
			if payload.ReasoningSummary != "" && !reasoningDelta[key] {
				s.notifyLocked("session/update", acpThoughtUpdate(session.id, session.reasoningMessageID(key), payload.ReasoningSummary))
			}
			for _, call := range payload.Message.ToolCalls {
				s.notifyLocked("session/update", acpToolCallUpdate(session.id, call))
			}
		case eventing.LLMToolCallResultCreated:
			var payload eventing.LLMToolCallResult
			if json.Unmarshal([]byte(event.Payload), &payload) == nil {
				s.notifyLocked("session/update", acpToolResultUpdate(session.id, payload))
			}
		case eventing.LLMToolCallRecoveryResultCreated:
			recovery, decodeErr := eventing.DecodeRecoveryPayload(event)
			if decodeErr == nil {
				s.notifyLocked("session/update", acpToolInterruptedUpdate(session.id, recovery.ToolCallID))
			}
		case eventing.LLMAgentResponseCreated:
			var payload eventing.LLMAgentResponse
			if json.Unmarshal([]byte(event.Payload), &payload) != nil {
				continue
			}
			key := acpReasoningKey(payload.ParentOriginatorID, payload.ParentOriginatorVersion)
			if payload.ReasoningSummary != "" && !reasoningDelta[key] {
				s.notifyLocked("session/update", acpThoughtUpdate(session.id, session.reasoningMessageID(key), payload.ReasoningSummary))
			}
		}
	}
	return nil
}

func acpThoughtUpdate(sessionID, messageID, text string) map[string]any {
	return map[string]any{"sessionId": sessionID, "update": map[string]any{"sessionUpdate": "agent_thought_chunk", "messageId": messageID, "content": acpTextContent(text)}}
}

func acpToolCallUpdate(sessionID string, call llm.ToolCall) map[string]any {
	return map[string]any{"sessionId": sessionID, "update": map[string]any{"sessionUpdate": "tool_call", "toolCallId": call.ID, "title": call.Name, "kind": acpToolKind(call.Name), "status": "in_progress", "rawInput": acpToolRawInput(call.Arguments)}}
}

func acpToolResultUpdate(sessionID string, result eventing.LLMToolCallResult) map[string]any {
	status := "completed"
	content := result.Message.Content
	if result.Error != "" {
		status = "failed"
		if content == "" {
			content = result.Error
		}
	}
	return map[string]any{"sessionId": sessionID, "update": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": result.Tool.ID, "status": status, "content": []map[string]any{{"type": "content", "content": acpTextContent(content)}}}}
}

func acpToolInterruptedUpdate(sessionID, toolCallID string) map[string]any {
	return map[string]any{"sessionId": sessionID, "update": map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": toolCallID, "status": "failed", "content": []map[string]any{{"type": "content", "content": acpTextContent(eventing.InterruptedToolResultMessage)}}}}
}

func acpSentFileUpdate(sessionID string, file eventing.SentFile) map[string]any {
	metadata := map[string]any{
		"id": file.ID, "kind": file.Kind, "name": file.Name, "mimeType": file.MimeType,
		"size": file.Size, "sha256": file.SHA256,
		"downloadPath": "sessions/" + sessionID + "/attachments/" + file.ID,
	}
	if file.Caption != "" {
		metadata["caption"] = file.Caption
	}
	if file.InReplyTo != "" {
		metadata["inReplyTo"] = file.InReplyTo
	}
	return map[string]any{"sessionId": sessionID, "update": map[string]any{"sessionUpdate": "caretz/file", "file": metadata}}
}

func (s *acpServer) notifyAgentText(sessionID, text string) {
	s.notify("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "agent_message_chunk",
			"content":       acpTextContent(text),
		},
	})
}

func (s *acpServer) notifyProvisionalDelta(sessionID, delta string) {
	s.notify("session/update", map[string]any{"sessionId": sessionID, "update": map[string]any{"sessionUpdate": "caretz/provisional_agent_message_chunk", "content": acpTextContent(delta)}})
}

func (s *acpServer) notifyProvisionalClear(sessionID string) {
	s.notify("session/update", map[string]any{"sessionId": sessionID, "update": map[string]any{"sessionUpdate": "caretz/provisional_agent_message_clear"}})
}

func (s *acpServer) notifySentFile(sessionID string, file eventing.SentFile) {
	s.notify("session/update", acpSentFileUpdate(sessionID, file))
}

func (s *acpServer) notifyAvailableCommands(session *acpSession) {
	if session == nil || session.slashCommands == nil {
		return
	}
	available := make([]map[string]any, 0, len(session.slashCommands.Commands()))
	for _, cmd := range session.slashCommands.Commands() {
		if cmd.Name() == "exit" {
			continue
		}
		command := map[string]any{
			"name":        cmd.Name(),
			"description": cmd.Description(),
			"input": map[string]any{
				"hint": cmd.Usage(),
			},
		}
		available = append(available, command)
	}
	s.notify("session/update", map[string]any{
		"sessionId": session.id,
		"update": map[string]any{
			"sessionUpdate":     "available_commands_update",
			"availableCommands": available,
		},
	})
}

func (s *acpServer) notifyAgentThought(session *acpSession, p eventing.LLMReasoningDelta) {
	key := acpReasoningKey(p.ParentOriginatorID, p.ParentOriginatorVersion)
	if strings.TrimSpace(p.Delta) != "" {
		session.markReasoningDelta(key)
		s.notifyAgentThoughtText(session.id, session.reasoningMessageID(key), p.Delta)
		return
	}
	if strings.TrimSpace(p.Text) != "" && !session.hasReasoningDelta(key) {
		s.notifyAgentThoughtText(session.id, session.reasoningMessageID(key), p.Text)
	}
}

func (s *acpServer) notifyReasoningSummary(session *acpSession, parentOriginatorID string, parentOriginatorVersion uint64, summary string) {
	if strings.TrimSpace(summary) == "" {
		return
	}
	key := acpReasoningKey(parentOriginatorID, parentOriginatorVersion)
	if session.hasReasoningDelta(key) {
		return
	}
	s.notifyAgentThoughtText(session.id, session.reasoningMessageID(key), summary)
}

func (s *acpServer) notifyAgentThoughtText(sessionID, messageID, text string) {
	s.notify("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"messageId":     messageID,
			"content":       acpTextContent(text),
		},
	})
}

func (s *acpServer) notifyToolCall(sessionID string, tc llm.ToolCall) {
	s.notify("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "tool_call",
			"toolCallId":    tc.ID,
			"title":         tc.Name,
			"kind":          acpToolKind(tc.Name),
			"status":        "pending",
			"rawInput":      acpToolRawInput(tc.Arguments),
		},
	})
	s.notify("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    tc.ID,
			"status":        "in_progress",
		},
	})
}

func (s *acpServer) notifyToolResult(sessionID string, result eventing.LLMToolCallResult) {
	status := "completed"
	if result.Error != "" {
		status = "failed"
	}
	content := result.Message.Content
	if result.Error != "" && content == "" {
		content = result.Error
	}
	s.notify("session/update", map[string]any{
		"sessionId": sessionID,
		"update": map[string]any{
			"sessionUpdate": "tool_call_update",
			"toolCallId":    result.Tool.ID,
			"status":        status,
			"content": []map[string]any{
				{"type": "content", "content": acpTextContent(content)},
			},
		},
	})
}

// notifyStatus shows a long-running command's progress, such as a local
// model server restart, as a thought chunk.
func (s *acpServer) notifyStatus(session *acpSession, p eventing.Status) {
	session.reasoningMu.Lock()
	session.statusSeq++
	seq := session.statusSeq
	session.reasoningMu.Unlock()
	s.notify("session/update", map[string]any{
		"sessionId": session.id,
		"update": map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"messageId":     fmt.Sprintf("status-%s-%d", p.Source, seq),
			"content":       acpTextContent(fmt.Sprintf("[%s] %s", p.Source, p.Message)),
		},
	})
}

func (s *acpServer) notifySubagentInvocation(session *acpSession, eventType string, eventVersion uint64, p eventing.SubagentInvocation) {
	kind, text := acpSubagentInvocationText(eventType, p)
	if text == "" {
		return
	}
	s.notify("session/update", map[string]any{
		"sessionId": session.id,
		"update": map[string]any{
			"sessionUpdate": "agent_thought_chunk",
			"messageId":     session.subagentMessageID(p, kind, eventVersion),
			"content":       acpTextContent(text),
		},
	})
}

func acpSubagentInvocationText(eventType string, p eventing.SubagentInvocation) (string, string) {
	switch eventType {
	case eventing.SubagentInvocationStarted:
		if strings.TrimSpace(p.Task) == "" {
			return "started", fmt.Sprintf("subagent %s started: %s", p.Name, p.ChildSessionID)
		}
		return "started", fmt.Sprintf("subagent %s started: %s\n%s", p.Name, p.ChildSessionID, p.Task)
	case eventing.SubagentInvocationProgress:
		return "", ""
	case eventing.SubagentInvocationCompleted:
		if strings.TrimSpace(p.Result) == "" {
			return "completed", fmt.Sprintf("subagent %s completed", p.Name)
		}
		return "completed", fmt.Sprintf("subagent %s completed: %s", p.Name, previewACPLine(p.Result, 220))
	case eventing.SubagentInvocationFailed:
		return "failed", fmt.Sprintf("subagent %s failed: %s", p.Name, p.Error)
	default:
		return "", ""
	}
}

func (s *acpSession) subagentMessageID(p eventing.SubagentInvocation, kind string, eventVersion uint64) string {
	id := p.ChildSessionID
	if id == "" {
		id = p.Name
	}
	if kind == "" {
		kind = "event"
	}
	if kind != "reasoning" {
		if eventVersion == 0 {
			s.reasoningMu.Lock()
			s.subagentSeq++
			eventVersion = s.subagentSeq
			s.reasoningMu.Unlock()
		}
		id = fmt.Sprintf("%s-%s-%d", id, kind, eventVersion)
	} else {
		id += "-reasoning"
	}
	return "subagent-" + strings.NewReplacer("/", "-", ":", "-", " ", "-").Replace(id)
}

func previewACPLine(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if limit <= 0 || len(text) <= limit {
		return text
	}
	return text[:limit] + "..."
}

func acpTextContent(text string) map[string]any {
	return map[string]any{"type": "text", "text": text}
}

func acpReasoningKey(parentOriginatorID string, parentOriginatorVersion uint64) string {
	return fmt.Sprintf("%s/%d", parentOriginatorID, parentOriginatorVersion)
}

func (s *acpSession) markReasoningDelta(key string) {
	s.reasoningMu.Lock()
	defer s.reasoningMu.Unlock()
	if s.reasoningDeltas == nil {
		s.reasoningDeltas = map[string]bool{}
	}
	s.reasoningDeltas[key] = true
}

func (s *acpSession) hasReasoningDelta(key string) bool {
	s.reasoningMu.Lock()
	defer s.reasoningMu.Unlock()
	return s.reasoningDeltas[key]
}

func (s *acpSession) reasoningMessageID(key string) string {
	s.reasoningMu.Lock()
	defer s.reasoningMu.Unlock()
	if s.reasoningIDs == nil {
		s.reasoningIDs = map[string]string{}
	}
	if id := s.reasoningIDs[key]; id != "" {
		return id
	}
	id := "reasoning-" + strings.NewReplacer("/", "-", ":", "-", " ", "-").Replace(key)
	s.reasoningIDs[key] = id
	return id
}

func acpToolKind(name string) string {
	switch name {
	case "Read", "Ls":
		return "read"
	case "Write", "Edit":
		return "edit"
	case "Grep", "Find":
		return "search"
	case "Bash":
		return "execute"
	case "WebSearch", "WebFetch":
		return "fetch"
	case "CallSubagent":
		return "subagent"
	case "UpdateTasks", "ListTasks":
		return "think"
	default:
		return "other"
	}
}

func acpToolRawInput(args string) any {
	var value any
	if err := json.Unmarshal([]byte(args), &value); err == nil {
		return value
	}
	return map[string]any{"arguments": args}
}

func (s *acpServer) notify(method string, params any) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	s.notifyLocked(method, params)
}

func (s *acpServer) notifyLocked(method string, params any) {
	s.writeJSON(acpNotification{JSONRPC: "2.0", Method: method, Params: params})
}

func (s *acpServer) acpDebugEnabled() bool {
	return s != nil && s.rt != nil && s.rt.Logger != nil && s.rt.Logger.Verbosity() > 0
}

func (s *acpServer) writeACPDebugLog(sessionID, format string, args ...any) {
	if !s.acpDebugEnabled() || strings.TrimSpace(sessionID) == "" {
		return
	}
	path := filepath.Join(s.rt.SessionsDir, acpAdapterType, sessionID, "debug.log")
	writeACPDebugLogFile(path, format, args...)
}

func (s *acpSession) debugf(format string, args ...any) {
	if s == nil || strings.TrimSpace(s.debugLogPath) == "" {
		return
	}
	writeACPDebugLogFile(s.debugLogPath, format, args...)
}

func writeACPDebugLogFile(path, format string, args ...any) {
	if strings.TrimSpace(path) == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	line := fmt.Sprintf(format, args...)
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	defer func() { _ = f.Close() }()
	_, _ = fmt.Fprintf(f, "%s %s\n", timestamp, line)
}

func (s *acpServer) writeResponse(resp acpResponse) {
	s.writeJSON(resp)
}

func (s *acpServer) writeJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, _ = s.out.Write(append(data, '\n'))
}

func (t *acpTurn) stopNow() {
	t.once.Do(func() {
		close(t.stop)
		t.cancel()
	})
}

func decodeParams(data json.RawMessage, target any) error {
	if len(data) == 0 || string(data) == "null" {
		data = []byte("{}")
	}
	return json.Unmarshal(data, target)
}

func acpAttachmentsDir(sessionsDir, sessionID string) string {
	return filepath.Join(sessionsDir, acpAdapterType, sessionID, "attachments")
}

func (f *acpFileSender) SendFile(ctx context.Context, sourcePath, caption, messageID string) (string, error) {
	if f == nil || f.server == nil || f.session == nil {
		return "", fmt.Errorf("ACP file sender is not ready")
	}
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	default:
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return "", fmt.Errorf("open file for sending: %w", err)
	}
	defer func() { _ = source.Close() }()
	info, err := source.Stat()
	if err != nil {
		return "", fmt.Errorf("stat file for sending: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("file is no longer a regular file: %s", sourcePath)
	}
	if info.Size() > tools.MaxSendFileBytes {
		return "", fmt.Errorf("file exceeds %d bytes: %s", tools.MaxSendFileBytes, sourcePath)
	}

	name := sanitizeACPAttachmentName(filepath.Base(sourcePath), "file")
	mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if separator := strings.IndexByte(mimeType, ';'); separator >= 0 {
		mimeType = mimeType[:separator]
	}
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	kind := "file"
	if strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		kind = "image"
	}
	id := uuid.NewV4().String()
	root := acpAttachmentsDir(f.server.rt.SessionsDir, f.sessionID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create sent-file directory: %w", err)
	}
	tmpDir, err := os.MkdirTemp(root, ".sent-file-*")
	if err != nil {
		return "", fmt.Errorf("create sent file: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()

	hash := sha256.New()
	destination, err := os.OpenFile(filepath.Join(tmpDir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", fmt.Errorf("create sent file: %w", err)
	}
	written, copyErr := io.Copy(io.MultiWriter(destination, hash), io.LimitReader(source, tools.MaxSendFileBytes+1))
	closeErr := destination.Close()
	if copyErr != nil {
		return "", fmt.Errorf("copy sent file: %w", copyErr)
	}
	if closeErr != nil {
		return "", fmt.Errorf("close sent file: %w", closeErr)
	}
	if written > tools.MaxSendFileBytes {
		return "", fmt.Errorf("file exceeds %d bytes: %s", tools.MaxSendFileBytes, sourcePath)
	}

	digest := fmt.Sprintf("%x", hash.Sum(nil))
	sidecar := acpAttachmentSidecar{ID: id, Kind: kind, Name: name, MimeType: mimeType, Size: written, SHA256: digest, File: name}
	metadata, _ := json.Marshal(sidecar)
	if err := os.WriteFile(filepath.Join(tmpDir, "metadata.json"), append(metadata, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("write sent-file metadata: %w", err)
	}
	finalDir := filepath.Join(root, id)
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return "", fmt.Errorf("commit sent file: %w", err)
	}
	file := eventing.SentFile{
		ID: id, Kind: kind, Name: name, MimeType: mimeType, Size: written, SHA256: digest,
		Caption: strings.TrimSpace(caption), InReplyTo: messageID,
	}
	if err := f.session.AppendAuditEvent(eventing.NewSentFile(f.sessionID, file)); err != nil {
		_ = os.RemoveAll(finalDir)
		return "", fmt.Errorf("persist sent file: %w", err)
	}
	return fmt.Sprintf("sent file %s", name), nil
}

func (s *acpServer) prepareACPPrompt(session *acpSession, p acpPromptParams) (string, string, []eventing.MessageAttachment, func(), error) {
	blocks := p.Prompt
	if len(blocks) == 0 {
		blocks = p.Content
	}
	if strings.TrimSpace(p.Message) != "" {
		return p.Message, p.Message, nil, func() {}, nil
	}
	var modelParts, displayParts []string
	var attachments []eventing.MessageAttachment
	var created []string
	cleanup := func() {
		for _, path := range created {
			_ = os.RemoveAll(path)
		}
	}
	var total int64
	for _, block := range blocks {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "", "text":
			if block.Text != "" {
				modelParts = append(modelParts, block.Text)
				displayParts = append(displayParts, block.Text)
			}
		case "resource_link":
			ref := blockResourceText(block)
			if ref != "" {
				modelParts = append(modelParts, ref)
				displayParts = append(displayParts, ref)
			}
		case "resource":
			if block.Resource == nil {
				cleanup()
				return "", "", nil, func() {}, fmt.Errorf("resource content is required")
			}
			if block.Resource.Text != "" {
				ref := blockResourceText(block)
				modelParts = append(modelParts, ref)
				displayParts = append(displayParts, ref)
				continue
			}
			if block.Resource.Blob == "" {
				cleanup()
				return "", "", nil, func() {}, fmt.Errorf("resource blob or text is required")
			}
			attachment, path, descriptor, err := s.storeACPAttachment(session, "file", block.Resource.URI, block.Resource.MimeType, block.Resource.Blob, &total)
			if err != nil {
				cleanup()
				return "", "", nil, func() {}, err
			}
			created = append(created, path)
			attachments = append(attachments, attachment)
			modelParts = append(modelParts, descriptor)
		case "image":
			attachment, path, descriptor, err := s.storeACPAttachment(session, "image", block.URI, block.MimeType, block.Data, &total)
			if err != nil {
				cleanup()
				return "", "", nil, func() {}, err
			}
			created = append(created, path)
			attachments = append(attachments, attachment)
			modelParts = append(modelParts, descriptor)
		default:
			cleanup()
			return "", "", nil, func() {}, fmt.Errorf("unsupported prompt content type %q", block.Type)
		}
	}
	return strings.Join(modelParts, "\n\n"), strings.Join(displayParts, "\n\n"), attachments, cleanup, nil
}

func (s *acpServer) storeACPAttachment(session *acpSession, kind, name, mimeType, encoded string, total *int64) (eventing.MessageAttachment, string, string, error) {
	if strings.TrimSpace(encoded) == "" {
		return eventing.MessageAttachment{}, "", "", fmt.Errorf("%s data is required", kind)
	}
	decodedLen := int64(base64.StdEncoding.DecodedLen(len(encoded)))
	// DecodedLen includes up to two padding bytes that DecodeString removes.
	if decodedLen > acpMaxAttachmentBytes+2 || *total+decodedLen > acpMaxAttachmentBytes+2 {
		return eventing.MessageAttachment{}, "", "", fmt.Errorf("attachments exceed the %d byte limit", acpMaxAttachmentBytes)
	}
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return eventing.MessageAttachment{}, "", "", fmt.Errorf("invalid %s base64 data", kind)
	}
	if int64(len(data)) > acpMaxAttachmentBytes || *total+int64(len(data)) > acpMaxAttachmentBytes {
		return eventing.MessageAttachment{}, "", "", fmt.Errorf("attachments exceed the %d byte limit", acpMaxAttachmentBytes)
	}
	mimeType = strings.TrimSpace(mimeType)
	if mimeType == "" {
		return eventing.MessageAttachment{}, "", "", fmt.Errorf("%s mimeType is required", kind)
	}
	if kind == "image" && !strings.HasPrefix(strings.ToLower(mimeType), "image/") {
		return eventing.MessageAttachment{}, "", "", fmt.Errorf("image mimeType must start with image/")
	}
	name = sanitizeACPAttachmentName(name, kind)
	id := uuid.NewV4().String()
	root := acpAttachmentsDir(s.rt.SessionsDir, session.id)
	if err := os.MkdirAll(root, 0755); err != nil {
		return eventing.MessageAttachment{}, "", "", fmt.Errorf("create attachment directory: %w", err)
	}
	tmpDir, err := os.MkdirTemp(root, ".attachment-*")
	if err != nil {
		return eventing.MessageAttachment{}, "", "", fmt.Errorf("create attachment: %w", err)
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	filePath := filepath.Join(tmpDir, name)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		return eventing.MessageAttachment{}, "", "", fmt.Errorf("write attachment: %w", err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	sidecar := acpAttachmentSidecar{ID: id, Kind: kind, Name: name, MimeType: mimeType, Size: int64(len(data)), SHA256: hash, File: name}
	metadata, _ := json.Marshal(sidecar)
	if err := os.WriteFile(filepath.Join(tmpDir, "metadata.json"), append(metadata, '\n'), 0644); err != nil {
		return eventing.MessageAttachment{}, "", "", fmt.Errorf("write attachment metadata: %w", err)
	}
	finalDir := filepath.Join(root, id)
	if err := os.Rename(tmpDir, finalDir); err != nil {
		return eventing.MessageAttachment{}, "", "", fmt.Errorf("commit attachment: %w", err)
	}
	*total += int64(len(data))
	attachment := eventing.MessageAttachment{ID: id, Kind: kind, Name: name, MimeType: mimeType, Size: int64(len(data)), SHA256: hash}
	descriptor := fmt.Sprintf("[ACP attachment]\ntype: %s\nmime_type: %s\nfilename: %s\nsize_bytes: %d\npath: %s", kind, mimeType, name, len(data), filepath.Join(finalDir, name))
	return attachment, finalDir, descriptor, nil
}

func sanitizeACPAttachmentName(name, kind string) string {
	name = strings.ReplaceAll(strings.TrimSpace(name), "\\", "/")
	name = filepath.Base(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	name = strings.Trim(b.String(), ".-_")
	if name == "" {
		if kind == "image" {
			return "image"
		}
		return "attachment"
	}
	return name
}

func acpUserContent(sessionID string, message eventing.LogMessage) map[string]any {
	text := message.DisplayContent
	if text == "" && len(message.Attachments) == 0 {
		text = message.Content
	}
	content := acpTextContent(text)
	if len(message.Attachments) == 0 {
		return content
	}
	items := make([]map[string]any, 0, len(message.Attachments))
	for _, attachment := range message.Attachments {
		items = append(items, map[string]any{
			"id": attachment.ID, "kind": attachment.Kind, "name": attachment.Name,
			"mimeType": attachment.MimeType, "size": attachment.Size, "sha256": attachment.SHA256,
			"downloadPath": "sessions/" + sessionID + "/attachments/" + attachment.ID,
		})
	}
	content["_meta"] = map[string]any{"caretz": map[string]any{"attachments": items}}
	return content
}

func blockResourceText(block acpContentBlock) string {
	if block.Resource != nil {
		if block.Resource.Text != "" {
			if block.Resource.URI != "" {
				return block.Resource.URI + "\n\n" + block.Resource.Text
			}
			return block.Resource.Text
		}
		return block.Resource.URI
	}
	if block.URI != "" {
		return block.URI
	}
	return block.Name
}

func invalidParams(err error) *acpError {
	return &acpError{Code: -32602, Message: err.Error()}
}

func internalError(err error) *acpError {
	return &acpError{Code: -32603, Message: err.Error()}
}
