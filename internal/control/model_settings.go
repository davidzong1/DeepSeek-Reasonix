package control

// TracksModelSettings reports whether the host supplied a live configuration
// observer. Unmanaged controllers have no settings I/O to perform on admission.
func (c *Controller) TracksModelSettings() bool { return c.modelSettings.current != nil }

// ModelSettingsState compares this immutable runtime with current disk config.
// It is intentionally separate from provider-visible messages and metadata.
func (c *Controller) ModelSettingsState() (applied, desired string, err error) {
	if c.modelSettings.current == nil {
		return "", "", nil
	}
	desired, err = c.modelSettings.current()
	return c.modelSettings.revision, desired, err
}

// ModelSettingsSourceRevision identifies an immutable Desktop resolver bundle.
// It is transport bookkeeping only, never part of the conversation.
func (c *Controller) ModelSettingsSourceRevision() string { return c.modelSettings.sourceRevision }

// controllerModelSettings keeps the immutable snapshot and its host admission
// callback together for the lifetime of one controller. The callback is guarded
// by Controller.mu; snapshot fields are immutable after construction.
type controllerModelSettings struct {
	revision            string
	sourceRevision      string
	current             func() (string, error)
	beforeInboxDispatch func(*Controller) (func(), error)
}

func newControllerModelSettings(opts Options) controllerModelSettings {
	return controllerModelSettings{
		revision:            opts.ModelSettingsRevision,
		sourceRevision:      opts.ModelSettingsSourceRevision,
		current:             opts.ModelSettingsCurrent,
		beforeInboxDispatch: opts.BeforeInboxDispatch,
	}
}
