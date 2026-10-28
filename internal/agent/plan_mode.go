package agent

import (
	"regexp"
	"strings"

	"github.com/makkalot/caretz/lib/llm"
)

const (
	planModeNotice  = `<caretz-mode mode="plan">Plan mode is on. Do not edit files or make persistent workspace changes; inspect with read-only tools and propose implementation steps instead of applying them. Edit, Write, and SendFile calls fail and Bash runs in a read-only sandbox until the user switches back to build mode.</caretz-mode>`
	buildModeNotice = `<caretz-mode mode="build">Plan mode is off; build mode is on. You may edit files and run commands that change the workspace again.</caretz-mode>`
)

var modeNoticePattern = regexp.MustCompile(`\n*<caretz-mode mode="(plan|build)">[^<]*</caretz-mode>`)

// SetPlanMode switches the session between plan and build mode. Tool
// definitions and the system prompt stay unchanged: the next user message
// tells the model which mode is active, and mutating tool calls fail while
// plan mode is on. Registries cloned from the session's, such as subagents',
// switch with it.
func (a *AgentLoop) SetPlanMode(enabled bool) {
	if a.toolRegistry != nil {
		a.toolRegistry.SetPlanMode(enabled)
	}
}

// PlanMode reports whether the session is in plan mode.
func (a *AgentLoop) PlanMode() bool {
	return a.toolRegistry.PlanMode()
}

// withModeNotice appends a mode notice to user message content when the mode
// differs from the one the model was last told about. A conversation without
// a notice is in build mode.
func (a *AgentLoop) withModeNotice(content string) string {
	plan := a.PlanMode()
	if announcedPlanMode(a.activeMessages()) == plan {
		return content
	}
	if plan {
		return content + "\n\n" + planModeNotice
	}
	return content + "\n\n" + buildModeNotice
}

// modeNoticeVariants lists every form withModeNotice can give content.
func modeNoticeVariants(content string) []string {
	return []string{content, content + "\n\n" + planModeNotice, content + "\n\n" + buildModeNotice}
}

func announcedPlanMode(messages []llm.Message) bool {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != llm.RoleUser {
			continue
		}
		matches := modeNoticePattern.FindAllStringSubmatch(messages[i].Content, -1)
		if len(matches) > 0 {
			return matches[len(matches)-1][1] == "plan"
		}
	}
	return false
}

// StripModeNotice removes the mode notices caretz appends to user messages so
// replayed conversations show what the user typed.
func StripModeNotice(content string) string {
	if !strings.Contains(content, "<caretz-mode ") {
		return content
	}
	return modeNoticePattern.ReplaceAllString(content, "")
}
