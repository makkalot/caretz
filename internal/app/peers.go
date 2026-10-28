package app

import (
	"context"
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
	return append([]string{"caretz", peerAdapter, l.def.Name}, l.flags...)
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
			b.WriteString("\n  " + herdr.ShellQuote(botCommand("caretz", def, rootFlags, rt.ConfigRoot, rt.ConfigRootSupplied)))
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
			return peers.NewFactory(peers.Config{
				RunDir:       runDir,
				Endpoint:     func(id string) string { return peers.SessionEndpoint(adapter, id) },
				Label:        peers.RootLabel,
				Peers:        listPeers,
				LockEndpoint: true,
				Logf:         logger.Info,
			})
		},
	}
	if start != nil {
		options.OnStart = func() {
			startPeerBots(rt, start, logger)
		}
	}
	return options
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
	runDir, err := peers.OpenRunDir(c.configRoot, c.workspace)
	if err != nil {
		return slashcmd.Result{}, err
	}
	var b strings.Builder
	for _, def := range defs {
		endpoint := peers.BotEndpoint(def.Name)
		status := "offline (start with: caretz peer " + def.Name + ")"
		if def.Name == c.self {
			status = fmt.Sprintf("this session (pid %d)", os.Getpid())
		} else if pid, live := runDir.Live(endpoint); live {
			status = fmt.Sprintf("running (pid %d)", pid)
		}
		fmt.Fprintf(&b, "%s: %s", def.Name, status)
		if pending := runDir.PendingCount(endpoint); pending > 0 {
			fmt.Fprintf(&b, ", %d queued", pending)
		}
		if def.Description != "" {
			fmt.Fprintf(&b, "\n  %s", def.Description)
		}
		b.WriteString("\n")
	}
	return slashcmd.Result{Message: strings.TrimRight(b.String(), "\n")}, nil
}
