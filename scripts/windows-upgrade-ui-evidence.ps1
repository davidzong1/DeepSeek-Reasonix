# The selector is deliberately scoped to the rendered transcript. Sidebar topic
# titles and offscreen accessibility nodes are not evidence of restored history.
function Get-UpgradeUIDescendants($element) {
  return $element.FindAll([Windows.Automation.TreeScope]::Descendants, [Windows.Automation.Condition]::TrueCondition)
}

function Invoke-UpgradeUIButton($element) {
  $pattern = $element.GetCurrentPattern([Windows.Automation.InvokePattern]::Pattern)
  $pattern.Invoke()
}

function Invoke-PendingHistoricalSession($root) {
  foreach ($element in (Get-UpgradeUIDescendants $root)) {
    $current = $element.Current
    if ($current.AutomationId -eq 'reasonix-prepare-restored-session' -and -not $current.IsOffscreen -and
        $current.BoundingRectangle.Width -gt 0 -and $current.BoundingRectangle.Height -gt 0) {
      Invoke-UpgradeUIButton $element
      return $true
    }
  }
  return $false
}

function Test-VisibleUpgradeHistory($root, [string]$text) {
  if ([string]::IsNullOrWhiteSpace($text)) { return $false }
  $descendants = @(Get-UpgradeUIDescendants $root)
  foreach ($element in $descendants) {
    $current = $element.Current
    if ($current.IsOffscreen -or $current.BoundingRectangle.Width -le 0 -or $current.BoundingRectangle.Height -le 0) { continue }
    # Old content alone cannot prove recovery: reject a still-loading/failed
    # recovery surface and the obsolete chat notice that originally hid this bug.
    if (([string]$current.AutomationId).StartsWith('reasonix-session-recovery-') -or
        $current.AutomationId -eq 'reasonix-prepare-restored-session' -or
        ([string]$current.Name).Contains('Failed to load conversation history.') -or
        ([string]$current.Name).Contains('加载会话历史失败。') -or
        ([string]$current.Name).Contains('載入會話歷史失敗。')) { return $false }
  }
  foreach ($transcript in $descendants) {
    if (-not ([string]$transcript.Current.AutomationId).StartsWith('reasonix-chat-transcript-') -or $transcript.Current.IsOffscreen) { continue }
    foreach ($element in (Get-UpgradeUIDescendants $transcript)) {
      $current = $element.Current
      if (-not $current.IsOffscreen -and $current.BoundingRectangle.Width -gt 0 -and $current.BoundingRectangle.Height -gt 0 -and
          $current.Name.Contains($text)) { return $true }
    }
  }
  return $false
}
