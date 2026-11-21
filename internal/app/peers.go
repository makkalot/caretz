package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/makkalot/caretz/internal/agent"
	"github.com/makkalot/caretz/internal/config"
	"github.com/makkalot/caretz/internal/herdr"
	"github.com/makkalot/caretz/internal/log"
	"github.com/makkalot/caretz/internal/output"
	"github.com/makkalot/caretz/internal/peers"
	"github.com/makkalot/caretz/internal/sessiontools"
	"github.com/makkalot/caretz/internal/slashcmd"
	"github.com/makkalot/caretz/internal/subagents"
	"github.com/makkalot/caretz/internal/tmux"
)

const peerAdapter = "peer"

// sessionSelectionFlags pick or shape one particular session, so a bot never
// inherits them from the session that starts it.
var sessionSelectionFlags = []string{"p", "prompt-file", "s", "adapter", "incognito", "version", "start-peers", "main-peer"}

// peerLaunch is the state of a `caretz peer <name>` process.
type peerLaunch struct {
	def     subagents.Definition
	runDir  *peers.RunDir
	lock    *peers.Lock
	mainDir string
	// workDir is the bot's writable workspace: mainDir unless the definition
	// confines it with `workspace:`.
	workDir string
	// flags are the command-line flags the bot was started with.
	flags []string
	// explicitModel is set when --model or --reasoning was given, which wins
	// over the definition's model and reasoning.
	explicitModel bool
}

// modelSettings applies the definition's model and reasoning, which win over
// a saved /model choice unless --model or --reasoning was given.
func (l *peerLaunch) modelSettings(fileConfig config.Config, settings runtimeSettings) (runtimeSettings, error) {
	if l.explicitModel {
		return settings, nil
	}
	reasoning := strings.TrimSpace(l.def.Reasoning)
	if ref := strings.TrimSpace(l.def.Model); ref != "" {
		if reasoning == "" {
			reasoning = settings.ReasoningEffort
		}
		restored, err := settingsForSessionRuntime(fileConfig, settings, sessionRuntimeState{ModelRef: ref, ReasoningEffort: reasoning})
		if err != nil {
			return settings, fmt.Errorf("bot %s: %w", l.def.Name, err)
		}
		return restored, nil
	}
	if reasoning != "" {
		settings.ReasoningEffort = reasoning
	}
	return settings, nil
}

// peerDefinitions lists the configured peer bots the active profile allows.
func peerDefinitions(agentsDirs, allowed []string) func() ([]subagents.Definition, error) {
	return func() ([]subagents.Definition, error) {
		defs, err := subagents.DiscoverFromDirs(agentsDirs)
		if err != nil {
			return nil, err
		}
		return subagents.Peers(filterAgents(defs, allowed)), nil
	}
}

// isPeerBot reports whether the workspace the flags select defines a peer bot
// called name.
func isPeerBot(flags *baseFlagValues, name string) bool {
	cwd, configRoot, err := flagRoots(flags)
	if err != nil {
		return false
	}
	homeAgentsDir, err := config.HomeAgentsDir()
	if err != nil {
		return false
	}
	configRootSupplied := flags.ConfigRootFlags.ConfigRootSet || strings.TrimSpace(os.Getenv("CARETZ_CONFIG_ROOT")) != ""
	def, err := subagents.LoadFromDirs(agentDirsForRuntime(homeAgentsDir, cwd, configRoot, configRootSupplied), name)
	return err == nil && def.IsPeer()
}

// configuredMainPeer reads peers.main from the config the flags select. It
// runs before the runtime is built, because the bot's profile shapes the
// runtime; load errors are left for buildRuntimeFromFlags to report.
func configuredMainPeer(flags *baseFlagValues) string {
	_, configRoot, err := flagRoots(flags)
	if err != nil {
		return ""
	}
	homeConfigPath, err := config.HomeConfigPath()
	if err != nil {
		return ""
	}
	selected := strings.TrimSpace(os.Getenv("CARETZ_PROFILE"))
	if flags.Changed["profile"] {
		selected = flags.Profile
	}
	configRootSupplied := flags.ConfigRootFlags.ConfigRootSet || strings.TrimSpace(os.Getenv("CARETZ_CONFIG_ROOT")) != ""
	cfg, err := config.LoadProfileWithTrust(homeConfigPath, filepath.Join(configRoot, "config.json"), selected, configRootSupplied)
	if err != nil || cfg.Runtime.Peers == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Runtime.Peers.Main)
}

// preparePeerLaunch loads and claims the bot named name. The returned lock
// must be held for the life of the process.
func preparePeerLaunch(rt *Runtime, name string) (*peerLaunch, error) {
	defs, err := peerDefinitions(rt.AgentsDirs, rt.Profile.Agents)()
	if err != nil {
		return nil, err
	}
	var def *subagents.Definition
	for i := range defs {
		if defs[i].Name == name {
			def = &defs[i]
		}
	}
	if def == nil {
		if loaded, err := subagents.LoadFromDirs(rt.AgentsDirs, name); err == nil && !loaded.IsPeer() {
			return nil, fmt.Errorf("agent %q is a subagent; set mode: peer in %s to run it as a bot", name, loaded.Path)
		}
		return nil, fmt.Errorf("no peer bot named %q is configured (looked in %s)", name, strings.Join(rt.AgentsDirs, ", "))
	}
	runDir, err := peers.OpenRunDir(rt.ConfigRoot, rt.CWD)
	if err != nil {
		return nil, err
	}
	lock, err := runDir.Lock(peers.BotEndpoint(name))
	if err != nil {
		return nil, err
	}
	launch := &peerLaunch{def: *def, runDir: runDir, lock: lock, mainDir: rt.CWD, workDir: rt.CWD}
	if dir := strings.TrimSpace(def.Workspace); dir != "" {
		if !filepath.IsAbs(dir) {
			dir = filepath.Join(rt.CWD, dir)
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			_ = lock.Release()
			return nil, fmt.Errorf("create bot workspace %s: %w", dir, err)
		}
		launch.workDir = filepath.Clean(dir)
	}
	return launch, nil
}

func (l *peerLaunch) confined() bool { return l.workDir != l.mainDir }

// configureSession applies the definition's prompt, permissions, and
// workspace confinement to the bot's session.
func (l *peerLaunch) configureSession(session *agent.AgentSession) error {
	registry := session.ToolRegistry().WithDisabledTools(subagents.DisabledTools(l.def.Permission))
	if l.confined() {
		// The bot's cwd is its only writable root; the main tree stays readable.
		registry = registry.WithReadableRoots(l.mainDir)
	}
	return session.SwitchRuntime(registry, l.systemPrompt())
}

func (l *peerLaunch) systemPrompt() string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(l.def.SystemPrompt))
	fmt.Fprintf(&b, "\n\nYou are the peer bot %q, a long-lived agent in this workspace. ", l.def.Name)
	b.WriteString("Other agents message you with SendMessage; each message arrives as a turn marked [Message ... from ...], and your final response to that turn is delivered to the sender automatically. ")
	b.WriteString("The user may also talk to you directly.")
	fmt.Fprintf(&b, "\n\nWorkspace: %s", l.mainDir)
	if l.confined() {
		fmt.Fprintf(&b, "\nWorking directory: %s\nYou may write only inside %s. The main workspace %s is read-only for you.", l.workDir, l.workDir, l.mainDir)
	}
	return b.String()
}

func (l *peerLaunch) integration(rt *Runtime, logger *log.Logger) sessiontools.Factory {
	name := l.def.Name
	return peers.NewFactory(peers.Config{
		RunDir:     l.runDir,
		Endpoint:   func(string) string { return peers.BotEndpoint(name) },
		Label:      name,
		Self:       name,
		AcceptFrom: l.def.AcceptFrom,
		RunTimeout: l.def.RunTimeout,
		Peers:      peerDefinitions(rt.AgentsDirs, rt.Profile.Agents),
		Logf:       logger.Info,
	})
}

// resumeCommand is how Herdr restores the bot's pane, with the same flags.
// Herdr requires a plain command name here, not a path.
func (l *peerLaunch) resumeCommand() []string {
	return append([]string{caretCommandName(), peerAdapter, l.def.Name}, l.flags...)
}

// caretCommandName is the running binary's base name, so a build installed
// under another name (for example caretz-durable) restarts and suggests
// itself rather than the default caretz on PATH.
func caretCommandName() string {
	executable, err := os.Executable()
	if err != nil {
		return "caretz"
	}
	return filepath.Base(executable)
}

func caretExecutable() string {
	executable, err := os.Executable()
	if err != nil {
		return "caretz"
	}
	return executable
}

// botCommand starts the bot named by def with the root session's flags. The
// definition's model, reasoning, and profile describe the bot, so they win
// over the root session's choice.
func botCommand(executable string, def subagents.Definition, rootFlags []string, configRoot string, configRootSupplied bool) []string {
	var owned []string
	if strings.TrimSpace(def.Model) != "" {
		owned = append(owned, "model")
	}
	if strings.TrimSpace(def.Reasoning) != "" {
		owned = append(owned, "reasoning")
	}
	if strings.TrimSpace(def.Profile) != "" {
		owned = append(owned, "profile")
	}
	command := []string{executable, peerAdapter, def.Name}
	hasConfigRoot := false
	for _, arg := range rootFlags {
		name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
		if slices.Contains(owned, name) {
			continue
		}
		hasConfigRoot = hasConfigRoot || name == "config-root"
		command = append(command, arg)
	}
	if configRootSupplied && !hasConfigRoot {
		// The root found its config root through CARETZ_CONFIG_ROOT, which a
		// new pane does not inherit.
		command = append(command, "--config-root", configRoot)
	}
	return command
}

// botSpawner opens one tab or window with a pane per command.
type botSpawner interface {
	SpawnTab(ctx context.Context, label, cwd string, layout herdr.Layout, commands []herdr.PaneCommand) ([]string, error)
}

// defaultBotTab holds the bots the peers layout does not place.
const defaultBotTab = "bots"

// botTab is one tab --start-peers opens and the offline bots it holds.
type botTab struct {
	label  string
	layout herdr.Layout
	defs   []subagents.Definition
}

// botTabs places offline bots into the tabs the peers config names, in config
// order. Bots no tab lists join a tab named "bots", added last unless the
// config names it. Tabs with no offline bot are dropped.
func botTabs(layout *config.Peers, offline []subagents.Definition) []botTab {
	var tabs []botTab
	placed := map[string]int{}
	if layout != nil {
		for _, tab := range layout.Tabs {
			tabs = append(tabs, botTab{label: strings.TrimSpace(tab.Name), layout: herdr.Layout(strings.TrimSpace(tab.Layout))})
			for _, bot := range tab.Bots {
				placed[strings.TrimSpace(bot)] = len(tabs) - 1
			}
		}
	}
	fallback := slices.IndexFunc(tabs, func(tab botTab) bool { return tab.label == defaultBotTab })
	for _, def := range offline {
		index, ok := placed[def.Name]
		if !ok {
			if fallback < 0 {
				tabs = append(tabs, botTab{label: defaultBotTab})
				fallback = len(tabs) - 1
			}
			index = fallback
		}
		tabs[index].defs = append(tabs[index].defs, def)
	}
	return slices.DeleteFunc(tabs, func(tab botTab) bool { return len(tab.defs) == 0 })
}

// spawnerFromEnvironment picks the multiplexer caretz runs in: Herdr first,
// then tmux. It returns nil when caretz runs in neither.
var spawnerFromEnvironment = func() botSpawner {
	if spawner := herdr.NewSpawnerFromEnvironment(herdr.SpawnerOptions{}); spawner != nil {
		return spawner
	}
	if spawner := tmux.NewSpawnerFromEnvironment(tmux.SpawnerOptions{}); spawner != nil {
		return spawner
	}
	return nil
}

// botStart is what --start-peers needs to bring offline bots up once the
// root session is on screen.
type botStart struct {
	runDir    *peers.RunDir
	defs      []subagents.Definition
	rootFlags []string
	spawner   botSpawner
}

// prepareBotStart checks --start-peers before the session starts, so a request
// that cannot be honoured fails instead of opening a session without bots.
func prepareBotStart(rt *Runtime, rootFlags []string) (*botStart, error) {
	defs, err := peerDefinitions(rt.AgentsDirs, rt.Profile.Agents)()
	if err != nil {
		return nil, err
	}
	if len(defs) == 0 {
		return nil, fmt.Errorf("--start-peers: no peer bots are configured; add mode: peer to an agent in .caretz/agents")
	}
	runDir, err := peers.OpenRunDir(rt.ConfigRoot, rt.CWD)
	if err != nil {
		return nil, fmt.Errorf("--start-peers: %w", err)
	}
	start := &botStart{runDir: runDir, defs: defs, rootFlags: rootFlags}
	offline := start.offline()
	if len(offline) == 0 {
		return start, nil
	}
	if start.spawner = spawnerFromEnvironment(); start.spawner == nil {
		var b strings.Builder
		b.WriteString("--start-peers needs Herdr or tmux to open the bots' panes. Start them in separate terminals instead:")
		for _, def := range offline {
			b.WriteString("\n  " + herdr.ShellQuote(botCommand(caretCommandName(), def, rootFlags, rt.ConfigRoot, rt.ConfigRootSupplied)))
		}
		return nil, fmt.Errorf("%s", b.String())
	}
	return start, nil
}

func (s *botStart) offline() []subagents.Definition {
	var offline []subagents.Definition
	for _, def := range s.defs {
		if _, live := s.runDir.Live(peers.BotEndpoint(def.Name)); !live {
			offline = append(offline, def)
		}
	}
	return offline
}

// rootPeerOptions enables messaging for an interactive root session and, with
// start, starts any configured bot that is not already running.
func rootPeerOptions(rt *Runtime, logger *log.Logger, stderr io.Writer, start *botStart) tuiPeerOptions {
	listPeers := peerDefinitions(rt.AgentsDirs, rt.Profile.Agents)
	defs, err := listPeers()
	if err != nil || len(defs) == 0 {
		return tuiPeerOptions{}
	}
	runDir, err := peers.OpenRunDir(rt.ConfigRoot, rt.CWD)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "Warning: peer bots disabled: %v\n", err)
		return tuiPeerOptions{}
	}
	options := tuiPeerOptions{
		Integration: func(adapter string) sessiontools.Factory {
			return rootPeerFactory(runDir, adapter, listPeers, logger.Info)
		},
	}
	if start != nil {
		options.OnStart = func() {
			startPeerBots(rt, start, logger)
		}
	}
	return options
}

// runPeerACP serves the peer bot name over ACP on stdin and stdout. The bot
// runs and answers its messages from start to exit; clients attach to its
// session with session/load.
func runPeerACP(ctx context.Context, name string, baseFlags baseFlagValues, logger *log.Logger, stdin io.Reader, stdout, stderr io.Writer, opts Options, allowedModels []string) int {
	if baseFlags.Incognito {
		output.Errorf(stderr, "peer bots keep durable sessions and cannot run incognito")
		return 2
	}
	if err := applyPeerProfile(&baseFlags, name); err != nil {
		output.Errorf(stderr, "%v", err)
		return 1
	}
	rt, code := buildRuntimeFromFlags(ctx, baseFlags, logger, stderr, opts)
	if code != 0 || rt == nil {
		return code
	}
	if rt.Incognito {
		output.Errorf(stderr, "peer bots keep durable sessions and cannot run incognito")
		return 2
	}
	launch, err := preparePeerLaunch(rt, name)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return 1
	}
	defer func() { _ = launch.lock.Release() }()
	launch.explicitModel = baseFlags.Changed["model"] || baseFlags.Changed["reasoning"]
	rt.printModelNotices(stderr)
	server, err := NewACPServer(rt, allowedModels)
	if err != nil {
		output.Errorf(stderr, "%v", err)
		return 1
	}
	acp := server.(*acpServer)
	acp.peer = launch
	if err := acp.ServeACP(ctx, stdin, stdout); err != nil {
		output.Errorf(stderr, "%v", err)
		return 1
	}
	return 0
}

// rootPeerFactory is the messaging integration of a root session of adapter:
// it may message every bot and receives their replies on its own endpoint.
func rootPeerFactory(runDir *peers.RunDir, adapter string, listPeers func() ([]subagents.Definition, error), logf func(string, ...any)) sessiontools.Factory {
	return peers.NewFactory(peers.Config{
		RunDir:       runDir,
		Endpoint:     func(id string) string { return peers.SessionEndpoint(adapter, id) },
		Label:        peers.RootLabel,
		Peers:        listPeers,
		LockEndpoint: true,
		Logf:         logf,
	})
}

// acpPeerIntegration returns the messaging integration for an ACP session
// working in cwd, or nil when that workspace defines no peer bots.
func acpPeerIntegration(rt *Runtime, cwd string, agentsDirs []string) (sessiontools.Factory, error) {
	listPeers := peerDefinitions(agentsDirs, rt.Profile.Agents)
	defs, err := listPeers()
	if err != nil || len(defs) == 0 {
		return nil, err
	}
	runDir, err := peers.OpenRunDir(rt.ConfigRoot, cwd)
	if err != nil {
		return nil, err
	}
	return rootPeerFactory(runDir, acpAdapterType, listPeers, rt.Logger.Info), nil
}

// startPeerBots opens the configured tabs with a pane per offline bot. Only
// one root session spawns at a time.
func startPeerBots(rt *Runtime, start *botStart, logger *log.Logger) {
	offline := start.offline()
	if len(offline) == 0 || start.spawner == nil {
		return
	}
	runDir := start.runDir
	spawnLock, err := runDir.Lock("spawner")
	if err != nil {
		return
	}
	executable := caretExecutable()
	tabs := botTabs(rt.Profile.Peers, offline)
	// Start where the root was started so relative flag paths such as
	// --workspace and --read-root resolve the same way.
	paneDir, err := os.Getwd()
	if err != nil {
		paneDir = rt.CWD
	}
	go func() {
		defer func() { _ = spawnLock.Release() }()
		spawned := false
		for _, tab := range tabs {
			commands := make([]herdr.PaneCommand, 0, len(tab.defs))
			for _, def := range tab.defs {
				commands = append(commands, herdr.PaneCommand{Label: def.Name, Command: botCommand(executable, def, start.rootFlags, rt.ConfigRoot, rt.ConfigRootSupplied)})
			}
			if _, err := start.spawner.SpawnTab(context.Background(), tab.label, paneDir, tab.layout, commands); err != nil {
				logger.Info("peers: starting bots in tab %s failed: %v", tab.label, err)
				continue
			}
			spawned = true
		}
		if !spawned {
			return
		}
		// Hold the spawner lock until the bots claim their endpoints so a
		// second root session started meanwhile does not spawn duplicates.
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			if len(start.offline()) == 0 {
				return
			}
			time.Sleep(250 * time.Millisecond)
		}
	}()
}

// PeerBotStatus is one configured peer bot of a workspace and its state.
type PeerBotStatus struct {
	Name        string
	Description string
	// Running reports whether a process runs the bot, and PID which one.
	Running bool
	PID     int
	// Queued counts messages waiting in the bot's inbox.
	Queued int
	// Revision fingerprints the bot's definition file, so a supervisor can
	// tell when it changed and the bot needs a restart to pick it up.
	Revision string
}

// definitionRevision is the SHA-256 of a definition's file, or empty when it
// cannot be read.
func definitionRevision(def subagents.Definition) string {
	if def.Path == "" {
		return ""
	}
	data, err := os.ReadFile(def.Path)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// peerStatuses reports the liveness and inbox depth of defs in workspace.
func peerStatuses(configRoot, workspace string, defs []subagents.Definition) ([]PeerBotStatus, error) {
	if len(defs) == 0 {
		return nil, nil
	}
	runDir, err := peers.OpenRunDir(configRoot, workspace)
	if err != nil {
		return nil, err
	}
	statuses := make([]PeerBotStatus, 0, len(defs))
	for _, def := range defs {
		endpoint := peers.BotEndpoint(def.Name)
		pid, running := runDir.Live(endpoint)
		statuses = append(statuses, PeerBotStatus{
			Name:        def.Name,
			Description: def.Description,
			Running:     running,
			PID:         pid,
			Queued:      runDir.PendingCount(endpoint),
			Revision:    definitionRevision(def),
		})
	}
	slices.SortFunc(statuses, func(a, b PeerBotStatus) int { return strings.Compare(a.Name, b.Name) })
	return statuses, nil
}

// PeerBots lists the peer bots the runtime's workspace defines and its
// profile allows, sorted by name.
func (rt *Runtime) PeerBots() ([]PeerBotStatus, error) {
	defs, err := peerDefinitions(rt.AgentsDirs, rt.Profile.Agents)()
	if err != nil {
		return nil, err
	}
	return peerStatuses(rt.ConfigRoot, rt.CWD, defs)
}

// peersCommand lists configured bots with their liveness and inbox depth.
type peersCommand struct {
	configRoot, workspace string
	// self names the bot this session runs as; empty in a root session.
	self string
	list func() ([]subagents.Definition, error)
}

func (peersCommand) Name() string        { return "peers" }
func (peersCommand) Description() string { return "List peer bots and whether they are running" }
func (peersCommand) Usage() string       { return "/peers" }
func (peersCommand) Aliases() []string   { return []string{"bots"} }

func (c peersCommand) Run(_ context.Context, _ *agent.AgentSession, _ string) (slashcmd.Result, error) {
	defs, err := c.list()
	if err != nil {
		return slashcmd.Result{}, err
	}
	if len(defs) == 0 {
		return slashcmd.Result{Message: "No peer bots are configured. Add mode: peer to an agent in .caretz/agents."}, nil
	}
	statuses, err := peerStatuses(c.configRoot, c.workspace, defs)
	if err != nil {
		return slashcmd.Result{}, err
	}
	var b strings.Builder
	for _, bot := range statuses {
		status := "offline (start with: caretz peer " + bot.Name + ")"
		if bot.Name == c.self {
			status = fmt.Sprintf("this session (pid %d)", os.Getpid())
		} else if bot.Running {
			status = fmt.Sprintf("running (pid %d)", bot.PID)
		}
		fmt.Fprintf(&b, "%s: %s", bot.Name, status)
		if bot.Queued > 0 {
			fmt.Fprintf(&b, ", %d queued", bot.Queued)
		}
		if bot.Description != "" {
			fmt.Fprintf(&b, "\n  %s", bot.Description)
		}
		b.WriteString("\n")
	}
	return slashcmd.Result{Message: strings.TrimRight(b.String(), "\n")}, nil
}
