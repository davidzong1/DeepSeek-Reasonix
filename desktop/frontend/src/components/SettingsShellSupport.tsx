import { SettingsSelect } from "./SettingsSelect";
import { CircleAlert, CircleCheck, RefreshCw } from "lucide-react";
import { useT } from "../lib/i18n";
import { asArray } from "../lib/array";
import type { SandboxView, ShellCapabilityView } from "../lib/types";
import { CopyButton } from "./CopyButton";

// The Sandbox settings section's shell surface: interpreter preference, the
// current session's bound shell vs what a reload would pick, and diagnostics.
// Windows exposes native PowerShell runtimes only; legacy Bash discovery stays
// behind compatibility code and is not presented as a supported Agent runtime.

function effectiveShellLabel(value: string, t: ReturnType<typeof useT>): string {
  switch (value) {
    case "git-bash": return t("settings.effectiveShellGitBash");
    case "pwsh": return t("settings.effectiveShellPwsh");
    case "powershell": return t("settings.effectiveShellPowershell");
    case "bash": return t("settings.effectiveShellBash");
    case "zsh": return t("settings.effectiveShellZsh");
    case "sh": return t("settings.effectiveShellSh");
    case "auto": return t("common.auto");
    default: return value.trim() || t("common.none");
  }
}

function capabilityLabel(id: string, t: ReturnType<typeof useT>): string {
  switch (id) {
    case "git-bash": return t("settings.effectiveShellGitBash");
    case "powershell": return t("settings.effectiveShellPowershell");
    case "pwsh": return t("settings.effectiveShellPwsh");
    case "zsh": return t("settings.shellCapabilityZsh");
    case "sh": return t("settings.shellCapabilitySh");
    case "git": return t("settings.gitCapability");
    default: return t("settings.effectiveShellBash");
  }
}

function visibleCapabilities(capabilities: ShellCapabilityView[], windows: boolean): ShellCapabilityView[] {
  return capabilities.filter(({ id }) => windows
    ? id === "pwsh" || id === "powershell"
    : id === "bash" || id === "zsh" || id === "sh");
}

function selectedPreference(preference: string, windows: boolean): string {
  const normalized = preference.trim().toLowerCase();
  if (windows) return normalized === "pwsh" || normalized === "powershell" ? normalized : "auto";
  return normalized === "bash" ? "bash" : "auto";
}

function ShellRuntimeValue({ shell, capabilities, t }: {
  shell: string;
  capabilities: ShellCapabilityView[];
  t: ReturnType<typeof useT>;
}) {
  const capability = capabilities.find(({ id }) => id === shell);
  return (
    <div className="shell-runtime">
      <span>{effectiveShellLabel(shell, t)}</span>
      {capability?.path && <code>{capability.path}</code>}
    </div>
  );
}

function RepairCard({ message, guidance, busy, reloadSession }: {
  message: string;
  guidance?: { manager: string; command?: string } | null;
  busy: boolean;
  reloadSession: () => void;
}) {
  const t = useT();
  return (
    <div className="shell-support__card">
      <div className="shell-support__hint">{message}</div>
      {guidance?.command && (
        <>
          <div className="shell-support__repair-command">
            <code>{guidance.command}</code>
            <CopyButton text={guidance.command} className="btn btn--small" label={t("settings.shellCopyCommand")} />
          </div>
          <div className="shell-support__repair-safety">{t("settings.shellRepairCommandHint")}</div>
        </>
      )}
      <div className="shell-support__actions">
        <button type="button" className="btn btn--small" disabled={busy} onClick={reloadSession}>
          <RefreshCw size={13} aria-hidden="true" />
          <span>{t("settings.shellRepairReload")}</span>
        </button>
      </div>
    </div>
  );
}

function field(label: string, control: React.ReactNode, stacked = false) {
  return (
    <div className={`settings-field${stacked ? " settings-field--stacked" : ""}`}>
      <div className="settings-field__copy">
        <div className="settings-field__copy-body">
          <div className="settings-field__label">{label}</div>
        </div>
      </div>
      <div className="settings-field__control">{control}</div>
    </div>
  );
}

function DetectionRow({ cap, t }: { cap: ShellCapabilityView; t: ReturnType<typeof useT> }) {
  return (
    <div className="shell-capability__row">
      {cap.available ? <CircleCheck size={14} aria-hidden="true" /> : <CircleAlert size={14} aria-hidden="true" />}
      <span className="shell-capability__name">{capabilityLabel(cap.id, t)}</span>
      <span className="shell-capability__detail">
        {cap.available ? (cap.path ? t("settings.shellDetectedAt", { path: cap.path }) : t("settings.shellDetected")) : t("settings.shellNotDetected")}
      </span>
    </div>
  );
}

export function ShellInterpreterFields({
  sb,
  windows,
  busy,
  setShell,
  reloadSession,
}: {
  sb: SandboxView;
  windows: boolean;
  busy: boolean;
  setShell: (prefer: string) => void;
  reloadSession: () => void;
}) {
  const t = useT();
  const capabilities = visibleCapabilities(asArray(sb.shellCapabilities), windows);
  const preference = (sb.shell || "auto").trim().toLowerCase();
  const selected = selectedPreference(preference, windows);
  const currentShell = String(sb.effectiveShell || selected);
  const resolvedShell = String(sb.resolvedShell || selected);
  const autoLabel = windows ? t("settings.shellAutoWindows") : t("settings.shellAuto");

  return (
    <>
      {field(t(windows ? "settings.powershellRuntime" : "settings.shellInterpreter"),
        <SettingsSelect className="mem-select set-grow" value={preference} selectedLabel={preference === selected ? undefined : autoLabel} disabled={busy} onValueChange={(value) => setShell(value)}>
          <option value="auto">{autoLabel}</option>
          {windows ? (
            <>
              <option value="pwsh">{t("settings.shellPwsh")}</option>
              <option value="powershell">{t("settings.shellPowershell")}</option>
            </>
          ) : <option value="bash">{t("settings.shellBash")}</option>}
        </SettingsSelect>)}
      {field(t("settings.effectiveShell"),
        <div className="settings-readonly-field"><ShellRuntimeValue shell={currentShell} capabilities={capabilities} t={t} /></div>)}
      {sb.shellReloadRequired && field(t("settings.resolvedShell"),
        <div className="settings-readonly-field">
          <ShellRuntimeValue shell={resolvedShell} capabilities={capabilities} t={t} />
          <button type="button" className="btn btn--small set-shell-reload" disabled={busy} onClick={reloadSession}>
            <RefreshCw size={13} aria-hidden="true" />
            <span>{t("settings.shellReloadNow")}</span>
          </button>
        </div>)}
    </>
  );
}

export function ShellEnvironmentDetails({
  sb,
  windows,
  busy,
  effectiveWriteRoots,
  reloadSession,
}: {
  sb: SandboxView;
  windows: boolean;
  busy: boolean;
  effectiveWriteRoots: string[];
  reloadSession: () => void;
}) {
  const t = useT();
  const capabilities = visibleCapabilities(asArray(sb.shellCapabilities), windows);
  const git = sb.gitCapability ?? null;
  const bashMissing = !windows && !capabilities.some((capability) => capability.id === "bash" && capability.available);
  const nativeFallback = sb.resolvedShell === "zsh" || sb.resolvedShell === "sh";
  const powershellFallback = windows
    && selectedPreference(sb.shell || "auto", true) === "auto"
    && sb.resolvedShell === "powershell"
    && !capabilities.some((capability) => capability.id === "pwsh" && capability.available);
  const runtimeMissing = windows
    ? !capabilities.some((capability) => capability.available)
    : bashMissing && !nativeFallback;
  const needsAttention = Boolean(sb.shellReloadRequired || runtimeMissing || nativeFallback || powershellFallback);

  return (
    <details className="runtime-details" open={needsAttention || undefined}>
      <summary>{t("settings.runtimeDetails")}</summary>
      <div>
        {field(t("settings.shellDetection"),
          <div className="shell-support">
            <div className="settings-readonly-field shell-support__detection">
              {capabilities.map((capability) => <DetectionRow key={capability.id} cap={capability} t={t} />)}
            </div>
            {!windows && bashMissing && !nativeFallback && (
              <RepairCard message={t("settings.shellBashManualRepair")} guidance={sb.shellRepairGuidance ?? null} busy={busy} reloadSession={reloadSession} />
            )}
          </div>, true)}
        {git && field(t("settings.dependencyDetection"),
          <div className="shell-support">
            <div className="settings-readonly-field shell-support__detection">
              <DetectionRow cap={git} t={t} />
            </div>
            {!git.available && !windows && (
              <RepairCard message={t("settings.gitManualRepair")} guidance={sb.gitRepairGuidance ?? null} busy={busy} reloadSession={reloadSession} />
            )}
          </div>, true)}
        {field(t("settings.effectiveWriteRoots"),
          <div className="set-rules set-rules--readonly">
            <div className="set-rules__chips">
              {effectiveWriteRoots.length === 0 && <span className="mem-empty">{t("settings.noEffectiveWriteRoots")}</span>}
              {effectiveWriteRoots.map((path, index) => (
                <span className="set-rule set-rule--path" key={`${path}-${index}`}>{path}</span>
              ))}
            </div>
          </div>, true)}
        <footer>
          <button type="button" className="btn btn--small" disabled={busy} onClick={reloadSession}>
            <RefreshCw size={13} aria-hidden="true" />
            <span>{t("settings.reloadSessionConfig")}</span>
          </button>
        </footer>
      </div>
    </details>
  );
}
