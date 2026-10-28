# Migrating NoteBox 2.x settings to NoteBox 3

NoteBox 3 reads a nested `settings.json` instead of `settings.ini`.

## Key mapping

| Old key (`section.key`)             | New key (JSON path)                | Conversion                                  |
| ----------------------------------- | ---------------------------------- | ------------------------------------------- |
| `general.username`                  | `user.name`                        | string as is                                |
| `general.theme`                     | `appearance.theme`                 | string as is                                |
| `editor.font_size_pt`               | `appearance.font_size_px`          | points × 4/3, rounded to a whole number     |
| `editor.tab_width`                  | `editor.tab_width`                 | integer                                     |
| `editor.show_whitespace`            | `editor.show_whitespace`           | boolean                                     |
| `general.autosave`                  | `editor.autosave.enabled`          | boolean                                     |
| `general.autosave_interval_minutes` | `editor.autosave.interval_seconds` | minutes × 60, integer                       |
| `network.timeout_ms`                | `network.timeout_seconds`          | milliseconds ÷ 1000                         |
| `network.retries`                   | `network.retries`                  | integer                                     |
| `network.proxy`                     | `network.proxy`                    | string, or `null` when the old value is empty |

Old boolean values may be written as `yes`/`no`, `true`/`false` or `on`/`off`.
In JSON they must be real booleans.

## Removed settings

- `general.legacy_sync` was removed in NoteBox 3. Drop it.

## New settings

These did not exist in NoteBox 2.x. Add them with their defaults:

- `appearance.language`: `"en"`
- `editor.word_wrap`: `false`
- `network.offline_mode`: `false`
