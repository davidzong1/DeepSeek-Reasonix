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
`UNSUPPORTED_REASONING_EFFORT` before network I/O. Invalid declarations are also
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
`thinking = "disabled"` entry reports `disabled` as its only ID, because that is
what validates a stored value: the stored verdict keeps admitting it, so a value
already on disk is judged exactly as it was before. The selection boundary is
narrower — `/effort`, the desktop menu, `--effort`, ACP session config and
subagent profiles accept `auto` and the levels that entry declares, and never the
pinned ID, which no `supported_efforts` edit makes selectable. The ID is retained
for stored values, not offered as a menu.

`reasoning_protocol = "none"` is the other fixed mode, and its vocabulary is
empty, so both boundaries refuse every level and only `auto` selects anything. An
endpoint whose adapter does send `disabled` as an effort — official DeepSeek, the
deepseek protocol, GLM, LongCat, MiniMax, anthropic's binary knob, and any entry
that declares it — keeps the level selectable, so a pinned entry there accepts
`/effort disabled` instead of refusing it.

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
| Provider TOML | Same fields and valid IDs; no automatic file rewrite |
| Desktop `EffortInfo.options` | Optional additive metadata; `levels` remains for older clients |
| New frontend / older backend | Falls back to the older `levels` field |
| Remote model descriptors | Existing `Efforts` declarations remain authoritative |
| Provider-visible history | No prompt, tool schema, or reasoning-history rewrite |

Default requests retain existing serialization. Explicitly changing an effort can
change provider cache behavior; the contract itself does not add prompt bytes.
The experimental governor checks declared capability before applying its low
request override. This change does not introduce automatic cross-model effort
migration or copy Harness's request journal architecture.

The design is independently implemented for Reasonix, informed by
[DeepSeek Harness's adapter-owned reasoning contract](https://github.com/deepseek-ai/deepseek-harness/blob/d347e703908d0406b7a7ef80e3a0e594d86b2215/.agents/notes/implemented/architecture/2026-07-24-adapter-owned-reasoning-effort-capabilities.md).
No upstream implementation was copied.
