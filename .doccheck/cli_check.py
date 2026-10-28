#!/usr/bin/env python3
"""Validate every `caretz ...` command example in docs against the real CLI.

Flags are checked against the generated --help output for the root and each
subcommand. Subcommands, make targets, tool names and env vars are checked too.
"""
import json, os, re, shlex, subprocess, sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), ".."))
os.chdir(ROOT)
BIN = os.path.join(ROOT, "bin", "caretz")
INV = json.load(open(".doccheck/inventory.json"))

# ---- authoritative flags from generated command help -----------------------
def command_flags(*path):
    result = subprocess.run([BIN, *path, "--help"], capture_output=True, text=True)
    text = result.stdout + result.stderr
    return set(re.findall(r'^\s+-([a-z0-9-]+)', text, re.M))

ROOT_FLAGS = command_flags()
COMMAND_FLAGS = {
    ("setup",): command_flags("setup"),
    ("acp",): command_flags("acp"),
    ("session",): command_flags("session"),
    ("models",): command_flags("models"),
}
for action in ("list", "explore", "inspect", "repair", "trim"):
    COMMAND_FLAGS[("session", action)] = command_flags("session", action)
for kind in ("skill", "agent"):
    COMMAND_FLAGS[(kind,)] = command_flags(kind)
    for action in ("install", "upgrade"):
        COMMAND_FLAGS[(kind, action)] = command_flags(kind, action)
COMMAND_FLAGS[("local",)] = command_flags("local")
for action in ("start", "stop", "restart", "status", "logs", "models"):
    COMMAND_FLAGS[("local", action)] = command_flags("local", action)
for action in ("set", "rm", "list"):
    COMMAND_FLAGS[("auth", action)] = command_flags("auth", action)

SESSION_ACTIONS = {"list", "explore", "inspect", "repair", "trim"}
RESOURCE_ACTIONS = {"install", "upgrade"}
LOCAL_ACTIONS = {"start", "stop", "restart", "status", "logs", "models"}
AUTH_ACTIONS = {"set", "rm", "list"}
SUBCOMMANDS = ("session", "skill", "agent", "acp", "setup", "local", "models", "auth")

# ---- known-good vocabularies ------------------------------------------------
MAKE_TARGETS = set(re.findall(r'^([a-z][a-z0-9-]*):', open("Makefile").read(), re.M))
env_sources = ["internal/app/main.go", "internal/app/runtime_config.go", "internal/config/config.go", "internal/config/credentials.go","internal/sandbox/network_proxy.go", "internal/sandbox/socks_connect.go", "install.sh"]
ENV_VARS = set()
for f in env_sources:
    ENV_VARS |= set(re.findall(r'\b(CARETZ_[A-Z0-9_]+|OPENAI_[A-Z_]+|TELEGRAM_[A-Z_]+|SLACK_[A-Z_]+)\b', open(f).read()))
TOOL_NAMES = set()
for d in ("internal/tools", "lib/tools", "internal/commands"):
    if not os.path.isdir(d):
        continue
    for f in sorted(os.listdir(d)):
        if f.endswith(".go") and not f.endswith("_test.go"):
            TOOL_NAMES |= set(re.findall(r'Name:\s*"([A-Za-z]+)"', open(os.path.join(d, f)).read()))

issues = []

def block_cmds(text, fname, line):
    """Yield (cmd, context) for each shell command line, joining \\ continuations."""
    lines = text.split("\n")
    buf = ""
    start = line
    for i, raw in enumerate(lines):
        s = raw.strip()
        if s.startswith("#") or not s:
            if buf:
                yield buf, start
                buf = ""
            continue
        if s.endswith("\\"):
            if not buf:
                start = line + i
            buf += s[:-1].strip() + " "
            continue
        if buf:
            yield (buf + s), start
            buf = ""
        else:
            yield s, line + i
    if buf:
        yield buf, start

inline = []  # inline `caretz ...` spans in prose
for b in INV:
    if b["lang"] not in ("sh", "bash", "console", "shell"):
        continue
    text = open(b["path"]).read()
    for cmd, ln in block_cmds(text, b["file"], b["line"]):
        # strip trailing shell comments
        cmd = re.sub(r'\s+#.*$', '', cmd).strip()
        try:
            toks = shlex.split(cmd)
        except ValueError:
            toks = cmd.split()
        if not toks:
            continue
        # make targets
        if toks[0] == "make":
            for t in toks[1:]:
                if t.startswith("-"):
                    continue
                if "=" in t:
                    continue
                if t not in MAKE_TARGETS:
                    issues.append((b["file"], ln, "unknown make target: make %s" % t))
            continue
        if toks[0] not in ("caretz", "./caretz", "./bin/caretz", "bin/caretz", "$HOME/bin/caretz", "caretz.exe"):
            continue
        # caretz command
        i = 1
        sub = None
        actions = []
        if i < len(toks) and not toks[i].startswith("-"):
            if toks[i] in SUBCOMMANDS:
                sub = toks[i]
                i += 1
                if sub in ("session", "local", "auth") and i < len(toks) and not toks[i].startswith("-"):
                    actions.append(toks[i]); i += 1
                elif sub in ("skill", "agent") and i < len(toks) and not toks[i].startswith("-"):
                    actions.append(toks[i]); i += 1
        flags = [t for t in toks[i:] if t.startswith("-")]
        path = (sub, *actions) if sub else ()
        allowed = set(COMMAND_FLAGS.get(path, ROOT_FLAGS if not sub else set()))
        for f in flags:
            name = f.lstrip("-").split("=")[0]
            if name in ("h", "help"):
                continue
            if name not in allowed:
                issues.append((b["file"], ln, "unknown flag --%s in: %s" % (name, cmd)))
        if sub == "session" and actions:
            if actions[0] not in SESSION_ACTIONS:
                issues.append((b["file"], ln, "unknown session action: %s" % actions[0]))
        if sub == "local" and actions:
            if actions[0] not in LOCAL_ACTIONS:
                issues.append((b["file"], ln, "unknown local action: %s" % actions[0]))
        if sub == "auth" and actions:
            if actions[0] not in AUTH_ACTIONS:
                issues.append((b["file"], ln, "unknown auth action: %s" % actions[0]))
        if sub in ("skill", "agent") and actions:
            if actions[0] not in RESOURCE_ACTIONS:
                issues.append((b["file"], ln, "unknown %s action: %s" % (sub, actions[0])))
        # --tools values
        for m in re.finditer(r'(?:^|\s)--?tools[=\s]+([^\s]+)', cmd):
            for t in m.group(1).split(","):
                if t and not t.startswith("-") and t not in TOOL_NAMES:
                    issues.append((b["file"], ln, "unknown tool name in --tools: %s (%s)" % (t, cmd)))

# inline code spans in prose
for b in INV:
    pass
for md in ["README.md"] + sorted(os.path.join(r, n) for r, _, ns in os.walk("docs") for n in ns if n.endswith(".md")) + \
           ["site/content/_index.md", "site/content/docs/_index.md"]:
    for n, line in enumerate(open(md).read().split("\n"), 1):
        for span in re.findall(r'`([^`]+)`', line):
            if span.startswith("caretz ") or span.startswith("caretz\t"):
                for cmd, ln in block_cmds(span, md, n):
                    toks = cmd.split()
                    path = ()
                    if len(toks) > 1 and toks[1] in SUBCOMMANDS:
                        path = (toks[1],)
                        if len(toks) > 2 and not toks[2].startswith("-"):
                            candidate = (toks[1], toks[2])
                            if candidate in COMMAND_FLAGS:
                                path = candidate
                    allowed = COMMAND_FLAGS.get(path, ROOT_FLAGS if not path else set()) | {"help"}
                    for t in toks:
                        if t.startswith("--"):
                            nm = t[2:].split("=")[0]
                            if nm not in allowed:
                                label = " ".join(path) if path else "root"
                                issues.append((md, n, "unknown %s flag in prose: %s" % (label, t)))
            for ev in re.findall(r'\b(CARETZ_[A-Z0-9_]+|OPENAI_[A-Z_]+)\b', span):
                if ev not in ENV_VARS:
                    issues.append((md, n, "unknown env var in prose: %s" % ev))
            for mt in re.findall(r'\bmake ([a-z][a-z0-9-]*)\b', span):
                if mt not in MAKE_TARGETS:
                    issues.append((md, n, "unknown make target in prose: make %s" % mt))

print("root flags known: %d" % len(ROOT_FLAGS))
print("tool names: %s" % ", ".join(sorted(TOOL_NAMES)))
print("make targets: %d" % len(MAKE_TARGETS))
print("env vars: %s" % ", ".join(sorted(ENV_VARS)))
print()
seen = set()
for f, ln, msg in issues:
    key = (f, ln, msg)
    if key in seen:
        continue
    seen.add(key)
    print("  %s:%d  %s" % (f, ln, msg))
print("\n%d unique issue(s)" % len(seen))
sys.exit(1 if seen else 0)
