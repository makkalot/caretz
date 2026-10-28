package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

type openAIProvider struct {
	apiKey          string
	model           string
	baseURL         string
	httpClient      *http.Client
	requestTimeout  time.Duration
	reasoningEffort string
	protocol        string
	maxAttempts     int
	initialBackoff  time.Duration
	maxBackoff      time.Duration
	logf            func(string, ...any)
}

const (
	DefaultRequestTimeout = 5 * time.Minute
	DefaultMaxAttempts    = 3
	DefaultInitialBackoff = 1 * time.Second
	DefaultMaxBackoff     = 1 * time.Minute
	openCodeUserAgent     = "caretz"
)

type OpenAIProviderOptions struct {
	RequestTimeout        time.Duration
	DisableRequestTimeout bool
	MaxAttempts           int
	InitialBackoff        time.Duration
	MaxBackoff            time.Duration
	ReasoningEffort       string
	Protocol              string
	Logf                  func(string, ...any)
	HTTPClient            *http.Client
}

const (
	ProtocolChatCompletions = "chat-completions"
	ProtocolResponses       = "responses"
)

type chatCompletionParams struct {
	Model           string         `json:"model"`
	Messages        []chatMessage  `json:"messages"`
	Tools           []chatTool     `json:"tools,omitempty"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
	Stream          bool           `json:"stream,omitempty"`
	StreamOptions   *streamOptions `json:"stream_options,omitempty"`
}

type streamOptions struct {
	IncludeUsage       bool `json:"include_usage,omitempty"`
	IncludeObfuscation bool `json:"include_obfuscation,omitempty"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    *string        `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatToolCall struct {
	ID       string           `json:"id"`
	Type     string           `json:"type"`
	Function chatFunctionCall `json:"function"`
}

type chatFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatTool struct {
	Type     string              `json:"type"`
	Function chatFunctionToolDef `json:"function"`
}

type chatFunctionToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type responsesRequest struct {
	Model         string                `json:"model"`
	Instructions  string                `json:"instructions,omitempty"`
	Input         []responsesInputItem  `json:"input"`
	Tools         []responsesTool       `json:"tools,omitempty"`
	Reasoning     responsesReasoning    `json:"reasoning,omitempty"`
	Stream        bool                  `json:"stream,omitempty"`
	StreamOptions responsesStreamOption `json:"stream_options,omitempty"`
}

type responsesStreamOption struct {
	IncludeObfuscation bool `json:"include_obfuscation"`
}

type responsesReasoning struct {
	Summary string `json:"summary,omitempty"`
	Effort  string `json:"effort,omitempty"`
}

type responsesInputItem struct {
	Type    string  `json:"type,omitempty"`
	Role    string  `json:"role,omitempty"`
	Content *string `json:"content,omitempty"`
	CallID  string  `json:"call_id,omitempty"`
	Name    string  `json:"name,omitempty"`
	Args    string  `json:"arguments,omitempty"`
	Output  *string `json:"output,omitempty"`
}

type responsesTool struct {
	Type        string         `json:"type"`
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
	Strict      bool           `json:"strict"`
}

type apiError struct {
	StatusCode int
	Message    string
	Type       string
	Code       string
	Body       string
}

// APIErrorStatus returns the HTTP status carried by a provider API error.
// It is useful to turn provider failures into actionable user-facing messages
// without depending on the error's formatted text.
func APIErrorStatus(err error) (int, bool) {
	var apiErr *apiError
	if !errors.As(err, &apiErr) {
		return 0, false
	}
	return apiErr.StatusCode, true
}

func (e *apiError) Error() string {
	msg := strings.TrimSpace(e.Message)
	if msg == "" {
		msg = strings.TrimSpace(e.Body)
	}
	if msg == "" {
		msg = http.StatusText(e.StatusCode)
	}
	return fmt.Sprintf("%d %s: %s", e.StatusCode, http.StatusText(e.StatusCode), msg)
}

// NewOpenAIProvider creates a new OpenAI-compatible provider.
func NewOpenAIProvider(apiKey, baseURL, model string) Provider {
	return NewOpenAIProviderWithOptions(apiKey, baseURL, model, OpenAIProviderOptions{})
}

// NewOpenAIProviderWithOptions creates a new OpenAI-compatible provider.
func NewOpenAIProviderWithOptions(apiKey, baseURL, model string, opts OpenAIProviderOptions) Provider {
	if opts.DisableRequestTimeout {
		opts.RequestTimeout = 0
	} else if opts.RequestTimeout <= 0 {
		opts.RequestTimeout = DefaultRequestTimeout
	}
	if opts.MaxAttempts <= 0 {
		opts.MaxAttempts = DefaultMaxAttempts
	}
	if opts.InitialBackoff <= 0 {
		opts.InitialBackoff = DefaultInitialBackoff
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = DefaultMaxBackoff
	}
	if opts.MaxBackoff < opts.InitialBackoff {
		opts.MaxBackoff = opts.InitialBackoff
	}
	httpClient := opts.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &openAIProvider{
		apiKey:          apiKey,
		model:           model,
		baseURL:         strings.TrimRight(baseURL, "/"),
		httpClient:      httpClient,
		requestTimeout:  opts.RequestTimeout,
		reasoningEffort: strings.TrimSpace(opts.ReasoningEffort),
		protocol:        normalizeProtocol(opts.Protocol),
		maxAttempts:     opts.MaxAttempts,
		initialBackoff:  opts.InitialBackoff,
		maxBackoff:      opts.MaxBackoff,
		logf:            opts.Logf,
	}
}

// ListOpenAIModels returns the model IDs advertised by an OpenAI-compatible
// provider's /models endpoint.
func ListOpenAIModels(ctx context.Context, apiKey, baseURL string, opts OpenAIProviderOptions) ([]string, error) {
	provider := NewOpenAIProviderWithOptions(apiKey, baseURL, "", opts).(*openAIProvider)
	return provider.listModels(ctx)
}

func (p *openAIProvider) listModels(ctx context.Context) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	requestCtx := ctx
	cancel := func() {}
	if p.requestTimeout > 0 {
		requestCtx, cancel = context.WithTimeout(ctx, p.requestTimeout)
	}
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, p.endpoint("/models"), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	p.setOpenCodeHeaders(req, false)
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, readAPIError(resp)
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 10<<20))
	if err := decoder.Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode models response: %w", err)
	}
	seen := make(map[string]bool, len(payload.Data))
	models := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, id)
	}
	sort.Strings(models)
	return models, nil
}

func (p *openAIProvider) GetModel() string {
	return p.model
}

func (p *openAIProvider) GetBaseURL() string {
	return p.baseURL
}

func (p *openAIProvider) ReasoningEffort() string {
	return p.reasoningEffort
}

func (p *openAIProvider) Chat(systemPrompt string, messages []Message, tools []ToolDefinition, onDelta func(string)) (Response, error) {
	return p.ChatContext(context.Background(), systemPrompt, messages, tools, onDelta)
}

func (p *openAIProvider) ChatContext(ctx context.Context, systemPrompt string, messages []Message, tools []ToolDefinition, onDelta func(string)) (Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	params := p.chatCompletionParams(systemPrompt, messages, tools)
	attempts := p.maxAttempts
	if attempts <= 0 {
		attempts = 1
	}

	var lastErr error
	attemptsMade := 0
	for attempt := 1; ; attempt++ {
		attemptsMade = attempt
		if err := ctx.Err(); err != nil {
			return Response{}, err
		}
		requestCtx := ctx
		cancel := func() {}
		if p.requestTimeout > 0 {
			requestCtx, cancel = context.WithTimeout(ctx, p.requestTimeout)
		}
		result, err := p.chatOnce(requestCtx, params, systemPrompt, messages, tools, onDelta)
		cancel()
		if err == nil {
			return result, nil
		}
		if ctx.Err() != nil {
			return Response{}, ctx.Err()
		}
		lastErr = err
		rateLimited := isRateLimitError(err)
		contextSize := IsContextSizeError(err)
		if contextSize || (!rateLimited && attempt >= attempts) {
			break
		}

		delay := p.retryDelay(attempt)
		if rateLimited {
			p.log("LLM request rate limited on attempt %d after timeout=%s: %v; retrying in %s", attempt, p.timeoutLabel(), err, delay)
		} else {
			p.log("LLM request attempt %d/%d failed after timeout=%s: %v; retrying in %s", attempt, attempts, p.timeoutLabel(), err, delay)
		}
		if handler := retryHandlerFromContext(ctx); handler != nil {
			handler(RetryInfo{
				Attempt:     attempt,
				Delay:       delay,
				Error:       err,
				RateLimited: rateLimited,
			})
		}
		if err := sleepContext(ctx, delay); err != nil {
			return Response{}, err
		}
	}

	if attemptsMade <= 1 {
		p.log("LLM request failed after 1 attempt with timeout=%s: %v", p.timeoutLabel(), lastErr)
		return Response{}, lastErr
	}
	p.log("LLM request failed after %d attempts with timeout=%s: %v", attemptsMade, p.timeoutLabel(), lastErr)
	return Response{}, fmt.Errorf("LLM request failed after %d attempts: %w", attemptsMade, lastErr)
}

func (p *openAIProvider) chatCompletionParams(systemPrompt string, messages []Message, tools []ToolDefinition) chatCompletionParams {
	params := chatCompletionParams{
		Model:    p.model,
		Messages: p.toOpenAIMessages(systemPrompt, messages),
		Tools:    p.toOpenAITools(tools),
	}
	if p.reasoningEffort != "" {
		params.ReasoningEffort = p.reasoningEffort
	}
	return params
}

func (p *openAIProvider) log(format string, args ...any) {
	if p.logf != nil {
		p.logf(format, args...)
	}
}

func (p *openAIProvider) timeoutLabel() string {
	if p.requestTimeout <= 0 {
		return "disabled"
	}
	return p.requestTimeout.String()
}

func (p *openAIProvider) chatOnce(ctx context.Context, params chatCompletionParams, systemPrompt string, messages []Message, tools []ToolDefinition, onDelta func(string)) (Response, error) {
	if p.supportsResponsesAPI() {
		return p.chatResponsesStreaming(ctx, systemPrompt, messages, tools, onDelta)
	}
	result, err := p.chatStreaming(ctx, params, true, onDelta)
	if err != nil {
		if isRateLimitError(err) || IsContextSizeError(err) {
			return Response{}, err
		}
		result, err = p.chatStreaming(ctx, params, false, onDelta)
		if err != nil {
			if isRateLimitError(err) || IsContextSizeError(err) {
				return Response{}, err
			}
			result, err = p.chatNonStreaming(ctx, params, onDelta)
		}
	}
	return result, err
}

func (p *openAIProvider) supportsResponsesAPI() bool {
	return p.protocol == ProtocolResponses
}

func normalizeProtocol(protocol string) string {
	if strings.EqualFold(strings.TrimSpace(protocol), ProtocolResponses) {
		return ProtocolResponses
	}
	return ProtocolChatCompletions
}

func (p *openAIProvider) chatResponsesStreaming(ctx context.Context, systemPrompt string, messages []Message, tools []ToolDefinition, onDelta func(string)) (Response, error) {
	params := p.responseNewParams(systemPrompt, messages, tools)
	params.Stream = true

	resp, err := p.postJSON(ctx, "/responses", params, true)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var contentBuf strings.Builder
	var reasoningBuf strings.Builder
	var usage Usage
	var toolCalls []ToolCall
	reasoningHandler := ReasoningHandlerFromContext(ctx)

	err = readSSE(resp.Body, func(data []byte) error {
		var event responsesStreamEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		switch event.Type {
		case "response.output_text.delta":
			if event.Delta != "" {
				contentBuf.WriteString(event.Delta)
				if onDelta != nil {
					onDelta(event.Delta)
				}
			}
		case "response.reasoning_summary_text.delta":
			if event.Delta != "" {
				reasoningBuf.WriteString(event.Delta)
				if reasoningHandler != nil {
					reasoningHandler(ReasoningUpdate{Delta: event.Delta})
				}
			}
		case "response.reasoning_summary_text.done":
			if strings.TrimSpace(event.Text) != "" {
				reasoningBuf.Reset()
				reasoningBuf.WriteString(event.Text)
				if reasoningHandler != nil {
					reasoningHandler(ReasoningUpdate{Text: event.Text, Done: true})
				}
			}
		case "response.output_item.done":
			if event.Item.Type == "function_call" {
				id := event.Item.CallID
				if id == "" {
					id = event.Item.ID
				}
				toolCalls = append(toolCalls, ToolCall{
					ID:        id,
					Name:      event.Item.Name,
					Arguments: event.Item.Arguments,
				})
			}
			if event.Item.Type == "reasoning" && reasoningBuf.Len() == 0 {
				for _, summary := range event.Item.Summary {
					if strings.TrimSpace(summary.Text) != "" {
						if reasoningBuf.Len() > 0 {
							reasoningBuf.WriteString("\n")
						}
						reasoningBuf.WriteString(summary.Text)
					}
				}
			}
		case "response.completed":
			usage = fromOpenAIResponseUsage(event.Response.Usage)
		case "response.failed":
			return fmt.Errorf("response failed: %s", event.Response.Error.Message)
		case "response.incomplete":
			return fmt.Errorf("response incomplete: %s", event.Response.IncompleteDetails.Reason)
		case "error":
			return fmt.Errorf("response error: %s", event.Message)
		}
		return nil
	})
	if err != nil {
		return Response{}, fmt.Errorf("responses stream error: %w", err)
	}

	content := contentBuf.String()
	reasoningSummary := strings.TrimSpace(reasoningBuf.String())
	if reasoningSummary != "" && reasoningHandler != nil {
		reasoningHandler(ReasoningUpdate{Text: reasoningSummary, Done: true})
	}
	message := Message{
		Role:      RoleAssistant,
		Content:   content,
		ToolCalls: toolCalls,
	}
	return Response{
		Content:          content,
		ToolCalls:        toolCalls,
		Usage:            usage,
		Message:          message,
		ReasoningSummary: reasoningSummary,
	}, nil
}

func (p *openAIProvider) responseNewParams(systemPrompt string, messages []Message, tools []ToolDefinition) responsesRequest {
	responseParams := responsesRequest{
		Model:        p.model,
		Instructions: systemPrompt,
		Input:        p.toResponsesInput(messages),
		Tools:        p.toResponsesTools(tools),
		Reasoning: responsesReasoning{
			Summary: "auto",
		},
		StreamOptions: responsesStreamOption{
			IncludeObfuscation: false,
		},
	}
	if p.reasoningEffort != "" {
		responseParams.Reasoning.Effort = p.reasoningEffort
	}
	return responseParams
}

func (p *openAIProvider) toResponsesInput(messages []Message) []responsesInputItem {
	var input []responsesInputItem
	for _, msg := range messages {
		switch msg.Role {
		case RoleSystem:
			input = append(input, responsesInputItem{Type: "message", Role: "system", Content: stringPointer(msg.Content)})
		case RoleAssistant:
			if strings.TrimSpace(msg.Content) != "" {
				input = append(input, responsesInputItem{Type: "message", Role: "assistant", Content: stringPointer(msg.Content)})
			}
			for _, tc := range msg.ToolCalls {
				input = append(input, responsesInputItem{Type: "function_call", CallID: tc.ID, Name: tc.Name, Args: sanitizeToolArgumentsForReplay(tc.Arguments)})
			}
		case RoleTool:
			input = append(input, responsesInputItem{Type: "function_call_output", CallID: msg.ToolCallID, Output: stringPointer(msg.Content)})
		case RoleUser:
			input = append(input, responsesInputItem{Type: "message", Role: "user", Content: stringPointer(msg.Content)})
		default:
			input = append(input, responsesInputItem{Type: "message", Role: "user", Content: stringPointer(msg.Content)})
		}
	}
	return input
}

func (p *openAIProvider) toResponsesTools(tools []ToolDefinition) []responsesTool {
	var result []responsesTool
	for _, tool := range tools {
		if tool.Name == "" {
			continue
		}
		result = append(result, responsesTool{
			Type:        "function",
			Name:        tool.Name,
			Description: tool.Description,
			Parameters:  normalizeToolParameters(tool.Parameters),
			Strict:      false,
		})
	}
	return result
}

func (p *openAIProvider) retryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return p.initialBackoff
	}
	delay := p.initialBackoff
	for i := 1; i < attempt; i++ {
		if delay >= p.maxBackoff/2 {
			return p.maxBackoff
		}
		delay *= 2
	}
	if delay > p.maxBackoff {
		return p.maxBackoff
	}
	return delay
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRateLimitError(err error) bool {
	var apiErr *apiError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusTooManyRequests {
		return true
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "429 too many requests") ||
		strings.Contains(text, "rate_limit_error") ||
		strings.Contains(text, "rate limit")
}

func (p *openAIProvider) chatStreaming(ctx context.Context, params chatCompletionParams, includeUsage bool, onDelta func(string)) (Response, error) {
	params.Stream = true
	if includeUsage {
		params.StreamOptions = &streamOptions{IncludeUsage: true}
	} else {
		params.StreamOptions = nil
	}

	resp, err := p.postJSON(ctx, "/chat/completions", params, true)
	if err != nil {
		return Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()

	var contentBuf strings.Builder
	var reasoningBuf strings.Builder
	var usage Usage
	acc := chatStreamAccumulator{toolCalls: map[int]*chatToolCall{}}
	reasoningHandler := ReasoningHandlerFromContext(ctx)

	err = readSSE(resp.Body, func(data []byte) error {
		var chunk chatCompletionChunk
		if err := json.Unmarshal(data, &chunk); err != nil {
			return err
		}
		if chunk.Usage != nil {
			usage = fromOpenAIUsage(*chunk.Usage)
		}
		if len(chunk.Choices) == 0 {
			return nil
		}
		choice := chunk.Choices[0]
		acc.addDelta(choice.Delta)
		if choice.Delta.ReasoningContent != "" {
			reasoningBuf.WriteString(choice.Delta.ReasoningContent)
			if reasoningHandler != nil {
				reasoningHandler(ReasoningUpdate{Delta: choice.Delta.ReasoningContent})
			}
		}
		if choice.Delta.Content != "" {
			if onDelta != nil {
				onDelta(choice.Delta.Content)
			}
			contentBuf.WriteString(choice.Delta.Content)
		}
		return nil
	})
	if err != nil {
		return Response{}, fmt.Errorf("stream error: %w", err)
	}

	if !acc.hasChoice {
		return Response{}, fmt.Errorf("no choices in response")
	}

	reasoningSummary := strings.TrimSpace(reasoningBuf.String())
	if reasoningSummary == "" {
		reasoningSummary = acc.messageReasoning
	}
	if reasoningSummary != "" && reasoningHandler != nil {
		reasoningHandler(ReasoningUpdate{Text: reasoningSummary, Done: true})
	}
	toolCalls := acc.toolCallList()
	message := Message{
		Role:      RoleAssistant,
		Content:   acc.content.String(),
		ToolCalls: toolCalls,
	}
	return Response{
		Content:          contentBuf.String(),
		ToolCalls:        toolCalls,
		Usage:            usage,
		Message:          message,
		ReasoningSummary: reasoningSummary,
	}, nil
}

func (p *openAIProvider) chatNonStreaming(ctx context.Context, params chatCompletionParams, onDelta func(string)) (Response, error) {
	params.Stream = false
	params.StreamOptions = nil

	resp, err := p.postJSON(ctx, "/chat/completions", params, false)
	if err != nil {
		return Response{}, fmt.Errorf("request error: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	var completion chatCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&completion); err != nil {
		return Response{}, err
	}
	if len(completion.Choices) == 0 {
		return Response{}, fmt.Errorf("no choices in response")
	}

	choice := completion.Choices[0]
	if onDelta != nil && choice.Message.Content != "" {
		onDelta(choice.Message.Content)
	}
	reasoningSummary := strings.TrimSpace(choice.Message.ReasoningContent)
	if reasoningSummary != "" {
		if reasoningHandler := ReasoningHandlerFromContext(ctx); reasoningHandler != nil {
			reasoningHandler(ReasoningUpdate{Text: reasoningSummary, Done: true})
		}
	}
	toolCalls := p.fromOpenAIToolCalls(choice.Message.ToolCalls)

	return Response{
		Content:          choice.Message.Content,
		ToolCalls:        toolCalls,
		Usage:            fromOpenAIUsage(completion.Usage),
		Message:          Message{Role: RoleAssistant, Content: choice.Message.Content, ToolCalls: toolCalls},
		ReasoningSummary: reasoningSummary,
	}, nil
}

func (p *openAIProvider) postJSON(ctx context.Context, path string, payload any, stream bool) (*http.Response, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
	p.setOpenCodeHeaders(req, true)

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 {
		return resp, nil
	}
	defer func() { _ = resp.Body.Close() }()
	return nil, readAPIError(resp)
}

func (p *openAIProvider) setOpenCodeHeaders(req *http.Request, includeSession bool) {
	if req == nil || !isOpenCodeURL(req.URL) {
		return
	}
	req.Header.Set("User-Agent", openCodeUserAgent)
	if includeSession {
		if sessionID := sessionIDFromContext(req.Context()); sessionID != "" {
			req.Header.Set("X-Opencode-Session", sessionID)
		}
	}
}

func isOpenCodeURL(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	return host == "opencode.ai" || strings.HasSuffix(host, ".opencode.ai")
}

func (p *openAIProvider) endpoint(path string) string {
	if p.baseURL == "" {
		return "https://api.openai.com/v1" + path
	}
	return strings.TrimRight(p.baseURL, "/") + path
}

func readAPIError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	apiErr := &apiError{StatusCode: resp.StatusCode, Body: string(body)}
	var payload struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		apiErr.Message = payload.Error.Message
		apiErr.Type = payload.Error.Type
		apiErr.Code = payload.Error.Code
	}
	return apiErr
}

func readSSE(r io.Reader, handle func([]byte) error) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	var dataLines []string
	flush := func() error {
		if len(dataLines) == 0 {
			return nil
		}
		data := strings.Join(dataLines, "\n")
		dataLines = dataLines[:0]
		if strings.TrimSpace(data) == "[DONE]" {
			return nil
		}
		return handle([]byte(data))
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, "data:") {
			dataLines = append(dataLines, strings.TrimSpace(strings.TrimPrefix(line, "data:")))
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	return flush()
}

func reasoningContentFromRaw(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var payload struct {
		ReasoningContent string `json:"reasoning_content"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	return payload.ReasoningContent
}

type openAIUsage struct {
	PromptTokens       int64 `json:"prompt_tokens"`
	CompletionTokens   int64 `json:"completion_tokens"`
	TotalTokens        int64 `json:"total_tokens"`
	PromptTokensDetail struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

type responseUsage struct {
	InputTokens        int64 `json:"input_tokens"`
	OutputTokens       int64 `json:"output_tokens"`
	TotalTokens        int64 `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int64 `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func fromOpenAIUsage(usage openAIUsage) Usage {
	return Usage{
		PromptTokens:       usage.PromptTokens,
		CachedPromptTokens: usage.PromptTokensDetail.CachedTokens,
		CompletionTokens:   usage.CompletionTokens,
		TotalTokens:        usage.TotalTokens,
	}
}

func fromOpenAIResponseUsage(usage responseUsage) Usage {
	return Usage{
		PromptTokens:       usage.InputTokens,
		CachedPromptTokens: usage.InputTokensDetails.CachedTokens,
		CompletionTokens:   usage.OutputTokens,
		TotalTokens:        usage.TotalTokens,
	}
}

func (p *openAIProvider) toOpenAIMessages(systemPrompt string, messages []Message) []chatMessage {
	var result []chatMessage
	if systemPrompt != "" {
		result = append(result, chatMessage{Role: "system", Content: stringPointer(systemPrompt)})
	}
	for _, msg := range messages {
		result = append(result, p.toOpenAIMessage(msg))
	}
	return result
}

func (p *openAIProvider) toOpenAIMessage(msg Message) chatMessage {
	switch msg.Role {
	case RoleSystem:
		return chatMessage{Role: "system", Content: stringPointer(msg.Content)}
	case RoleUser:
		return chatMessage{Role: "user", Content: stringPointer(msg.Content)}
	case RoleAssistant:
		if len(msg.ToolCalls) == 0 {
			return chatMessage{Role: "assistant", Content: stringPointer(msg.Content)}
		}
		var toolCalls []chatToolCall
		for _, tc := range msg.ToolCalls {
			toolCalls = append(toolCalls, chatToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: chatFunctionCall{
					Name:      tc.Name,
					Arguments: sanitizeToolArgumentsForReplay(tc.Arguments),
				},
			})
		}
		var content *string
		if msg.Content != "" {
			content = stringPointer(msg.Content)
		}
		return chatMessage{Role: "assistant", Content: content, ToolCalls: toolCalls}
	case RoleTool:
		return chatMessage{Role: "tool", Content: stringPointer(msg.Content), ToolCallID: msg.ToolCallID}
	default:
		return chatMessage{Role: "user", Content: stringPointer(msg.Content)}
	}
}

func stringPointer(value string) *string {
	return &value
}

func sanitizeToolArgumentsForReplay(arguments string) string {
	trimmed := strings.TrimSpace(arguments)
	if trimmed == "" {
		return "{}"
	}

	var object map[string]any
	if err := json.Unmarshal([]byte(trimmed), &object); err == nil {
		return trimmed
	}

	fallback, err := json.Marshal(map[string]string{
		"_raw_arguments": arguments,
	})
	if err != nil {
		return "{}"
	}
	return string(fallback)
}

func (p *openAIProvider) toOpenAITools(tools []ToolDefinition) []chatTool {
	var result []chatTool
	for _, tool := range tools {
		result = append(result, chatTool{
			Type: "function",
			Function: chatFunctionToolDef{
				Name:        tool.Name,
				Description: tool.Description,
				Parameters:  normalizeToolParameters(tool.Parameters),
			},
		})
	}
	return result
}

func normalizeToolParameters(parameters map[string]any) map[string]any {
	if parameters == nil {
		return nil
	}
	normalized, ok := normalizeSchemaValue(parameters).(map[string]any)
	if !ok {
		return parameters
	}
	return normalized
}

func normalizeSchemaValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, child := range v {
			out[key] = normalizeSchemaValue(child)
		}
		normalizeConflictingAnyOf(out)
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = normalizeSchemaValue(child)
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(v))
		for i, child := range v {
			if normalized, ok := normalizeSchemaValue(child).(map[string]any); ok {
				out[i] = normalized
			} else {
				out[i] = child
			}
		}
		return out
	default:
		return v
	}
}

func normalizeConflictingAnyOf(schema map[string]any) {
	if _, hasType := schema["type"]; !hasType {
		return
	}
	if _, hasAnyOf := schema["anyOf"]; !hasAnyOf {
		return
	}
	delete(schema, "anyOf")
}

func (p *openAIProvider) fromOpenAIToolCalls(toolCalls []chatToolCall) []ToolCall {
	var result []ToolCall
	for _, tc := range toolCalls {
		result = append(result, ToolCall{
			ID:        tc.ID,
			Name:      tc.Function.Name,
			Arguments: tc.Function.Arguments,
		})
	}
	return result
}

type chatCompletionResponse struct {
	Choices []struct {
		Message chatCompletionMessage `json:"message"`
	} `json:"choices"`
	Usage openAIUsage `json:"usage"`
}

type chatCompletionMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	ToolCalls        []chatToolCall `json:"tool_calls"`
	ReasoningContent string         `json:"reasoning_content"`
}

type chatCompletionChunk struct {
	Choices []struct {
		Delta chatCompletionDelta `json:"delta"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
}

type chatCompletionDelta struct {
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	ReasoningContent string              `json:"reasoning_content"`
	ToolCalls        []chatToolCallDelta `json:"tool_calls"`
}

type chatToolCallDelta struct {
	Index    int               `json:"index"`
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Function chatFunctionDelta `json:"function"`
}

type chatFunctionDelta struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatStreamAccumulator struct {
	hasChoice        bool
	content          strings.Builder
	messageReasoning string
	toolCalls        map[int]*chatToolCall
	order            []int
}

func (a *chatStreamAccumulator) addDelta(delta chatCompletionDelta) {
	a.hasChoice = true
	a.content.WriteString(delta.Content)
	if delta.ReasoningContent != "" {
		a.messageReasoning += delta.ReasoningContent
	}
	for _, tc := range delta.ToolCalls {
		call, ok := a.toolCalls[tc.Index]
		if !ok {
			call = &chatToolCall{Type: "function"}
			a.toolCalls[tc.Index] = call
			a.order = append(a.order, tc.Index)
		}
		if tc.ID != "" {
			call.ID = tc.ID
		}
		if tc.Type != "" {
			call.Type = tc.Type
		}
		if tc.Function.Name != "" {
			call.Function.Name += tc.Function.Name
		}
		if tc.Function.Arguments != "" {
			call.Function.Arguments += tc.Function.Arguments
		}
	}
}

func (a *chatStreamAccumulator) toolCallList() []ToolCall {
	var result []ToolCall
	for _, index := range a.order {
		call := a.toolCalls[index]
		if call == nil {
			continue
		}
		result = append(result, ToolCall{
			ID:        call.ID,
			Name:      call.Function.Name,
			Arguments: call.Function.Arguments,
		})
	}
	return result
}

type responsesStreamEvent struct {
	Type     string              `json:"type"`
	Delta    string              `json:"delta"`
	Text     string              `json:"text"`
	Message  string              `json:"message"`
	Item     responsesOutputItem `json:"item"`
	Response struct {
		Usage responseUsage `json:"usage"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		IncompleteDetails struct {
			Reason string `json:"reason"`
		} `json:"incomplete_details"`
	} `json:"response"`
}

type responsesOutputItem struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Summary   []struct {
		Text string `json:"text"`
	} `json:"summary"`
}
