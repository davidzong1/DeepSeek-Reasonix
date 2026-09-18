# Adapter-owned reasoning controls

Each protocol adapter registers a pure `ReasoningForConfig` resolver alongside
its provider factory. Its returned options are ordered IDs with display names and
optional descriptions. Core does not impose a global effort vocabulary. Resolved
clients expose a detached capability snapshot through `ReasoningProvider`.

Configuration, the desktop effort menu, CLI completion, the local model catalog,
and request validation use these declarations. Model overrides must be resolved
before querying capabilities. Extension providers own their declared `Efforts`
list; selection and request overrides are validated before sidecar stream I/O.

Explicit selections must match a declared ID exactly. Unsupported choices return
`UNSUPPORTED_REASONING_EFFORT` before network I/O. Missing capability metadata
returns `UNKNOWN_MODEL_REASONING` for an explicit choice, with recovery through
`auto` or a configured protocol/vocabulary. An empty list alone does not prove
that the model cannot think. Explicit `reasoning_protocol = "none"` declares
unsupported controls. Invalid declarations are also
rejected. No nearest-level mapping is performed. Binary protocols cannot acquire
a depth scale merely by listing depth values in `supported_efforts`.

## Where the vocabulary is enforced

One resolved vocabulary produces one verdict at every boundary that can carry an
effort, as long as that vocabulary names levels the endpoint can be asked for. A
level refused at one is refused at the others, and none of them rewrites the
value into a neighbour. A vocabulary naming the endpoint's fixed mode carries no
such level: it is the one case where the stored and the selected verdict differ,
and it is not an effort menu.

| Boundary | Entry point | Undeclared level |
| --- | --- | --- |
| Explicit selection | `/effort`, desktop menu, `--effort`, ACP session config, subagent profiles | refused |
| Stored configuration | provider assembly, then adapter construction | refused |
| Per-request override | `provider.Request.EffortOverride` | refused before HTTP |

`supported_efforts` is the provider-entry key that decides the vocabulary. A
non-empty list **replaces** the built-in one, so an endpoint with real depth
levels declares them there; the built-in scale for a known endpoint is not
consulted in addition. An empty or absent list means the endpoint offers no
depth control at all, and every level is refused.

Declared levels are lowercase. The TOML loader folds and de-duplicates the list,
and a selection is then matched against it exactly; a raw list assembled by hand,
bypassing that normalization, is not supported input.

One path keeps a stored value instead of refusing it, and it applies only to what
is already on disk, never to a selection just made:

- A saved DeepSeek `medium`/`xhigh` on an entry with no declared vocabulary keeps
  its historical `high` wire value (`migrateStoredDeepSeekEffort`). The same
  alias typed at `/effort` is refused.

The adapter additionally skips its own construction-time check for two settings
that declare no depth control: `thinking = "disabled"` and
`reasoning_protocol = "none"`. Neither skip rescues a stored level, because
provider assembly validates it first against that entry's vocabulary — empty for
`none`, `disabled`-only for `disabled` — so a stored `max` on either is refused
there rather than mapped. Both skips matter only to a caller that constructs the
adapter without provider assembly, and neither widens what a fresh selection may
pick.

That is also where a fixed mode and a selectable level part company. A pinned
`thinking = "disabled"` entry reports `disabled` as its only ID, whatever else is
declared, because that is what validates a stored value: the stored verdict keeps
admitting it, so a value already on disk is judged exactly as it was before. The
pin replaces the declared vocabulary rather than extending it, so an ordinary
level appended to `supported_efforts` never becomes selectable — the vocabulary
stays `disabled`-only, and a locked entry's menu offers `auto` alone.

Whether that ID is selectable turns on whether the endpoint really sends it. When
the adapter's own scale carries `disabled` — official DeepSeek, the deepseek
protocol, GLM, LongCat, MiniMax and anthropic's binary knob all do — or the entry
declares it in `supported_efforts`, the vocabulary is unlocked: `/effort disabled`
is accepted, the menu offers `auto` and `disabled`, and the level is sent as
stored. Only a vocabulary carrying `disabled` for no reason other than the pin is
locked, and there the ID is retained for stored values alone: the selection
boundary refuses it along with every other level.

`reasoning_protocol = "none"` is the other fixed mode, and its vocabulary is
empty, so both boundaries refuse every level and only `auto` selects anything.

The ACP per-session override drops to `auto` (`""`) when the selected model
cannot express it. That is the contract's meaning of "inherit the provider
default", not a different level.

`auto` remains the existing UI/CLI spelling for clearing an override; it is not an
adapter option and does not mean adaptive thinking. Request-level overrides use
an empty string to inherit configuration, not the literal `auto`. Existing load
normalization of retired stored `off` and letter case is retained. Existing valid
IDs and TOML field names remain unchanged. Saved DeepSeek `medium` and `xhigh`
values retain their historical `high` wire value when no explicit effort vocabulary
is declared; configuration storage is not rewritten. New explicit selections and
request overrides still reject undeclared aliases. Other unsupported aliases
produce an actionable error. Invalid configured
defaults remain visible for validation instead of falling back to another level.

| Boundary | Compatibility |
| --- | --- |
| Provider TOML | Existing fields and valid IDs remain readable; no load-time rewrite |
| Desktop `EffortInfo.options` | Optional additive metadata; `levels` remains for older clients |
| New frontend / older backend | Falls back to the older `levels` field |
| Remote model descriptors | Existing `Efforts` declarations remain authoritative |
| Provider-visible history | No prompt, tool schema, or reasoning-history rewrite |

Default requests retain existing serialization. Explicitly changing an effort can
change provider cache behavior; the contract itself does not add prompt bytes.
The experimental governor checks declared capability before applying its low
request override. This change does not copy Harness's request journal architecture.

`ResolveReasoningEntry` resolves model overrides, connection overrides, then
current built-in facts. Built-in defaults match the exact effective request URL,
API kind and model ID, including the Token Rhythm `deepseek-flash` alias;
renaming a connection does not change this match. An explicit different reasoning
protocol owns its vocabulary. UI metadata, startup validation and adapter
construction use the same resolved entry. Raw configuration retains only user
declarations, so inherited defaults follow catalog updates. Untagged historical
declarations remain explicit because their provenance cannot be recovered safely.

Full configuration writes include backward-readable model capability snapshots.
The optional `reasoning_defaults` marker records generated fields and a digest of
the reasoning declaration. Current readers peel only untouched generated fields;
older readers still see the original protocol/list/default fields. A legacy
writer dropping the marker, or changing the declaration without updating its
digest, makes those values explicit. Settings delta saves preserve unrelated and
unknown fields without materializing unchanged inherited values.

| Field or format | Old data | Current reader | Previous reader/writer | Result |
| --- | --- | --- | --- | --- |
| `reasoning_defaults` | Missing means explicit | Untouched marked fields inherit live defaults | Ignores marker; reads snapshot; marker loss preserves values as explicit | Downgrade remains readable |
| Model capability `reasoning.state` | Optional | `supported`, `unsupported`, or `unknown` | Ignores additive metadata | Older UI continues using its existing fields |
| Remote descriptor `ReasoningUnknown` | Missing means existing declaration semantics | Preserves unknown adapter metadata | Ignores additive flag | Existing `Efforts` remain valid |
| Session model/effort | Existing storage fields | Rebinds as one selection on model changes | Same persisted representation | No session migration |

Session effort belongs to its provider/model identity. Model changes clear
inherited effort even when both models accept the same ID; same-model explicit
choices remain strict and are never silently clamped. New drafts only inherit
effort from the same model. Boot accepts an optional runtime-only `EffortModel`
origin for inherited overrides. CLI, Desktop, ACP and serve paths preserve this
boundary; `auto` remains available to clear an override with unknown metadata.

The design is independently implemented for Reasonix, informed by
[DeepSeek Harness's adapter-owned reasoning contract](https://github.com/deepseek-ai/deepseek-harness/blob/d347e703908d0406b7a7ef80e3a0e594d86b2215/.agents/notes/implemented/architecture/2026-07-24-adapter-owned-reasoning-effort-capabilities.md).
No upstream implementation was copied.
