# Explicit local-provider classification

## Summary

Ask users adding an existing local provider whether it should count as local or hosted. A user-controlled server on another machine, including over Tailscale, can count as local regardless of its URL.

## Implementation changes

### Setup

- In the existing **Local provider** setup flow, ask once the connection details are final (after any "Edit connection details" loop, before saving), not right after the first base URL prompt:
  - **Local / self-hosted** — this machine or another server you control.
  - **Hosted service** — treat as cloud.
- Default the answer from the existing URL heuristic applied to the final URL. Explain that local/self-hosted models participate in “Use local” and “Always local here.”
- Escape/Ctrl-C at the new prompt cancels setup like the other prompts (`io.EOF`, nothing saved).
- When the user picks **Local / self-hosted** for a non-private host over plain `http://`, warn that workspace content travels unencrypted.
- The **Hosted provider** flow keeps its prompts unchanged but saves `local: false` silently, so a hosted entry on a loopback/private URL (for example a LiteLLM proxy forwarding to the cloud) is not classified local by URL.
- Quick-local setup stays unchanged (managed loopback server; URL detection applies).

### Config field

- Save an optional provider-level `local` boolean. `true` explicitly means local; `false` explicitly means hosted; omission preserves URL-based detection.
- `local: true` requires a configured `baseURL`; without one it is a load error, so an empty/default endpoint can never be marked local.
- `local` stays a provider field only, so the existing rule that a workspace `.caretz/config.json` may not contain `provider` keeps repositories from marking their own endpoints local.
- Persistence: add `Local *bool` to `config.SetupProvider` and write it in `SaveSetupProvider`. Every caller that rebuilds a provider with `replace=true` must pass the existing value through — in particular the local preset switch in `internal/app/local_switch.go`, which otherwise drops a hand-set `local` on each switch.
- Merging: `mergeProvider` carries `local` through, including explicit `false`. When an override layer (for example a `--config-root` config) sets `baseURL` without setting `local`, the merged `local` resets to nil (URL detection) rather than inheriting trust granted for the base layer's URL.
- A hand edit of `baseURL` keeps the existing `local` value; document that the flag follows the provider entry, so changing its URL means revisiting it.

### Classification

- Centralize classification in one config function, for example `cfg.IsLocalModel(ref, effectiveBaseURL)`:
  - The provider's `local` value applies only when the normalized effective base URL equals the provider's configured `baseURL` (same normalization as setup: trimmed, trailing `/` removed).
  - Any other effective URL — `--base-url`, `OPENAI_API_BASE_URL`, or no provider — uses `IsLocalEndpoint` only. An override never inherits the provider's classification.
- Replace every direct `IsLocalEndpoint`/base-URL-only check with it:
  - `needsCloudConsent`, `newCloudConsentQuestion`, `confirmCloudWorkspace`, `confirmCloudModelSwitch` (`internal/app/cloud_consent.go`) take the ref plus the effective URL.
  - Startup warning (`internal/app/runtime.go`), prompt-mode startup and `/models` switch (`internal/app/agent_command.go`), ACP startup and switch (`internal/app/acp.go`, using the post-`Prepare` config).
  - `Config.LocalSelectors` uses the provider's classification at its configured URL.
- Keep `IsLocalEndpoint` unchanged. In particular, do not extend it to 100.64/10 (CGNAT, shared with ISPs) or `*.ts.net`: Tailscale endpoints stay hosted unless marked local.
- Keep explicit directory allowlists authoritative: classifying a provider as local never bypasses an existing model rule.

### Saved rules

- “Always local here” keeps saving a snapshot of `LocalSelectors()` as `provider/*` selectors. Reclassifying a provider as hosted later does not remove it from rules already saved; existing allowlists are not migrated.
- Document this, and show a notice when the active rule allows a provider that is now classified hosted, so a "local-only" rule does not silently allow a cloud model.

### Docs

- Document the choice, manual `local` configuration, the override/merge rules above, and the saved-rule caveat in the existing own-server and model-rules pages.

## Out of scope

- Subagent and `/goal` role models go through `requireModel` only and get no cloud-consent check today. This plan does not add one; track it separately if wanted.

## Test plan

- Setup saves both answers, defaults from the final URL (including after "Edit connection details"), and cancels without saving.
- Hosted flow with a loopback URL saves `local: false` and requires cloud consent.
- A Tailscale IP or public hostname marked local appears among local choices and skips cloud consent; the same endpoint with `local` omitted still asks.
- A private or loopback endpoint marked hosted requires the existing cloud-consent behavior.
- Provider marked local with `--base-url` or `OPENAI_API_BASE_URL` pointing elsewhere still requires cloud consent (startup, prompt mode, ACP).
- A config-root override that changes `baseURL` without `local` falls back to URL detection.
- `local: true` without `baseURL` is a load error.
- `provider.local` in a workspace `.caretz/config.json` is still rejected.
- The local preset switch (`local_switch` save) keeps an existing `local` value.
- Omitted classification preserves current behavior, config merging preserves explicit `false`, and classification survives save/load.
- Explicit model rules still reject disallowed self-hosted models.
- Reclassifying a provider as hosted leaves saved rules unchanged and shows the hosted-provider notice.
- Run focused config/setup/model-policy/cloud-consent tests, then `make test` and `make doccheck`.

## Assumptions

- “Local” describes trust for model policy, including servers the user controls elsewhere.
- The new question appears only in the existing local-provider flow; the hosted flow records `local: false` without asking.
- Existing configurations require no migration; saved allowlists remain unchanged.
