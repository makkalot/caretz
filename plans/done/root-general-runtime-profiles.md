# Root General Profile with Advanced Operational Profiles

## Summary

Make `general` the reserved root profile and default runtime behavior. Every other profile inherits from it automatically.

Keep beginner-facing configuration focused on providers, `activeModel`, and per-model reasoning/context settings. Profiles remain optional and advanced.

Explicit CLI arguments always have final precedence over environment variables, project config, home config, and profiles.

## Configuration Model

- Keep these ordinary root fields:
  - `provider`
  - `activeModel`
  - `profile`
  - `profiles`
- Keep `reasoning`, `reasoningValues`, and context limits under provider models.
- Do not allow profiles to override model or reasoning selection.
- Move operational configuration exclusively into profiles:
  - `systemPrompt`
  - `tools`, `disabledTools`, `toolConfig`
  - `skills`, `agents`
  - `sandbox`
  - `execution.docker`
  - `tasks`
  - `output`
  - `plan`, `incognito`
  - request timeout/max-attempt policy
  - model-discovery policy
- Reject legacy top-level `sandbox`, `execution`, `tools`, `tasks`, and `output` with errors pointing to `profiles.general.<field>`.
- Keep setup and `/models` persistence writing root-level `activeModel`.

Advanced example:

```json
{
  "activeModel": "local/qwen",
  "profile": "coding",
  "provider": {
    "local": {
      "options": {
        "baseURL": "http://localhost:8000/v1"
      },
      "models": {
        "qwen": {
          "reasoning": "high",
          "limit": {
            "context": 60000
          }
        }
      }
    }
  },
  "profiles": {
    "general": {
      "output": {
        "mode": "compact"
      },
      "tasks": {
        "enabled": true
      }
    },
    "coding": {
      "skills": ["go-development"],
      "agents": ["reviewer"]
    }
  }
}
```

## Resolution and CLI Precedence

Resolve settings in this order:

1. Built-in `general`
2. Built-in selected profile
3. Home `general`
4. Home selected profile
5. Project `general`
6. Project selected profile
7. Environment variables
8. Explicit CLI arguments

CLI rules:

- Every explicitly supplied CLI flag wins for its corresponding field.
- Explicit false values such as `--plan=false`, `--incognito=false`, or `--tasks=false` override enabled profile values.
- Tool-policy CLI lists replace their corresponding configured list when supplied.
- `--tools` replaces the profile tool allowlist.
- `--disable-tool` replaces the profile tool denylist.
- `--read-root`, `--writable-root`, and `--network-allow` append grants to their respective profile lists.
- Docker publish flags replace their respective profile lists.
- Individual scalar flags override only their field. For example, `--docker-image` replaces the image while retaining other resolved Docker settings.
- Workspace and config-root flags are applied first because they determine which configuration files are loaded, but they remain authoritative.
- CLI conflicts and safety validation run against the final resolved values.

## Inheritance and Built-ins

- Select `general` when `profile` is omitted.
- Apply `general` only once when it is selected.
- Omitted fields inherit.
- Empty lists explicitly clear inherited lists.
- `["*"]` selects all available resources.
- Do not support arbitrary `extends` or deeper inheritance.
- Built-in behavior:
  - `general`: neutral prompt, all tools/skills/agents, and existing runtime defaults.
  - `coding`: inherits general, replaces the base prompt with the current coding prompt, enables persistent session execution, and allows common development/package hosts through the restricted network proxy.
  - `compact`: inherits general, keeps the neutral prompt, exposes only `Bash`, clears skills and agents, and disables tasks. Its allowlist also excludes schedule tools.
- Continue appending dynamic tool guidance, filtered skills, `AGENTS.md`, and workspace context to the resolved base prompt.
- Preserve tool and agent enforcement across CLI, TUI, ACP, completions, child sessions, and embedded runtimes.

## Documentation Strategy

- Do not mention profiles in the README introduction, feature overview, setup flow, or early configuration examples.
- Keep early documentation centered on `caretz setup`, providers, `activeModel`, and per-model reasoning/context configuration.
- Add one clearly labeled “Advanced runtime profiles” section near the end of the configuration guide.
- Move output, sandbox, Docker, task customization, prompt replacement, and tool/resource selection examples into that advanced section.
- Explain that most users never need a profile because `general` is automatic.
- Document the complete precedence chain and emphasize that explicitly provided CLI arguments always win.
- Update sandboxing, local-model, embedding, and CLI references only where they already discuss advanced runtime customization.

## Test Plan

- Verify omitted profile selection resolves to neutral built-in `general`.
- Verify `coding` changes only the inherited prompt and `compact` applies its reduced resource set.
- Prove project layers override home layers, including project `general` overriding home selected-profile values.
- Cover omitted, empty, and wildcard lists plus nested tool, sandbox, Docker, task, output, timeout, and discovery settings.
- Test every CLI-over-config class:
  - scalar values;
  - explicit false booleans;
  - allowlists and denylists;
  - sandbox roots and network lists;
  - Docker image, enablement, networking, and published ports;
  - tasks, output, timeout, and model discovery.
- Verify profile filters remain enforced for model tool calls, commands, completions, ACP sessions, and child sessions.
- Verify setup and model switching continue updating root `activeModel`.
- Verify removed top-level operational fields fail with actionable migration messages.
- Verify early documentation and README do not introduce profiles.
- Run `go test ./...`, `go vet ./...`, `make doccheck`, and `git diff --check`; preserve unrelated worktree files and do not commit.
