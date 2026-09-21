package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"reasonix/internal/browser"
)

const fileBrowserPreviewTimeout = 60 * time.Second

// FileBrowserPreviewRequest is the additive desktop RPC contract used by file
// links, present cards, automatic delivery, and the browser_preview tool.
// ExpectedSessionGeneration may be zero for an older caller; current callers
// send it whenever they already hold tab metadata.
type FileBrowserPreviewRequest struct {
	Source                    string `json:"source"`
	Path                      string `json:"path"`
	ToolCallID                string `json:"toolCallId,omitempty"`
	OperationID               string `json:"operationId"`
	ExpectedSessionGeneration uint64 `json:"expectedSessionGeneration,omitempty"`
	// UserInitiated is accepted only by the renderer RPC. It lets an explicit
	// refresh replace the file URL while the user owns the tab without granting
	// the agent control of that page. Agent tool requests never set this field.
	UserInitiated bool `json:"userInitiated,omitempty"`
}

type FileBrowserPreviewResult struct {
	TabID             string `json:"tabId"`
	URL               string `json:"url"`
	Status            string `json:"status"`
	Error             string `json:"error,omitempty"`
	SessionGeneration uint64 `json:"sessionGeneration"`
}

type fileBrowserPreviewBinding struct {
	BrowserTabID      string
	URL               string
	SessionGeneration uint64
}

type preparedFileBrowserPreview struct {
	url      string
	identity string
}

// OpenFileBrowserPreviewForTab validates the resource and opens it through the
// same task grant the agent browser tools use. That makes the visible page and
// the page the agent can inspect one and the same Electron WebContentsView.
func (a *App) OpenFileBrowserPreviewForTab(tabID string, request FileBrowserPreviewRequest) (FileBrowserPreviewResult, error) {
	ctx, cancel := context.WithTimeout(a.bootContext(), fileBrowserPreviewTimeout)
	defer cancel()
	return a.openFileBrowserPreview(ctx, tabID, request, nil)
}

func normalizeFileBrowserPreviewRequest(request FileBrowserPreviewRequest) (FileBrowserPreviewRequest, error) {
	request.Source = strings.TrimSpace(request.Source)
	request.Path = strings.TrimSpace(request.Path)
	request.OperationID = strings.TrimSpace(request.OperationID)
	if request.Source == "" {
		request.Source = "workspace"
	}
	if request.Source != "workspace" && request.Source != "presented" && request.Source != "reference" {
		return FileBrowserPreviewRequest{}, fmt.Errorf("unsupported file preview source %q", request.Source)
	}
	if request.Path == "" || request.OperationID == "" {
		return FileBrowserPreviewRequest{}, errors.New("path and operationId are required")
	}
	if request.Source == "presented" && strings.TrimSpace(request.ToolCallID) == "" {
		return FileBrowserPreviewRequest{}, errors.New("toolCallId is required for a presented file")
	}
	return request, nil
}

func (a *App) openFileBrowserPreview(ctx context.Context, tabID string, request FileBrowserPreviewRequest, supplied browser.Executor) (FileBrowserPreviewResult, error) {
	request, err := normalizeFileBrowserPreviewRequest(request)
	if err != nil {
		return FileBrowserPreviewResult{}, err
	}
	tab, generation, err := a.fileBrowserPreviewTab(tabID, request.ExpectedSessionGeneration)
	if err != nil {
		return FileBrowserPreviewResult{}, err
	}
	exec := supplied
	if exec == nil {
		exec = a.hostBrowserExecutorForTab(tab.ID)
	}
	if exec == nil {
		return FileBrowserPreviewResult{}, errors.New("the built-in browser is unavailable")
	}
	prepared, err := a.prepareFileBrowserPreview(tabID, request)
	if err != nil {
		return FileBrowserPreviewResult{}, err
	}
	key := strings.Join([]string{tabID, fmt.Sprint(generation), request.Source, strings.TrimSpace(request.ToolCallID), filepath.Clean(prepared.identity)}, "\x00")

	a.fileBrowserPreviewMu.Lock()
	if a.fileBrowserPreviews == nil {
		a.fileBrowserPreviews = map[string]fileBrowserPreviewBinding{}
	}
	var retiredURLs []string
	binding, reusable := a.fileBrowserPreviews[key]
	if reusable {
		tabs, listErr := exec.Tabs(ctx)
		if listErr != nil {
			a.fileBrowserPreviewMu.Unlock()
			a.revokeWorkspaceBrowserPreview(prepared.url)
			return FileBrowserPreviewResult{}, listErr
		}
		found := false
		for _, candidate := range tabs {
			if candidate.ID != binding.BrowserTabID {
				continue
			}
			found = true
			// Navigating away explicitly releases the file binding. A later
			// delivery opens a fresh tab instead of overwriting the user's page.
			if candidate.URL != binding.URL {
				delete(a.fileBrowserPreviews, key)
				retiredURLs = append(retiredURLs, binding.URL)
				reusable = false
			}
			break
		}
		if !found {
			delete(a.fileBrowserPreviews, key)
			retiredURLs = append(retiredURLs, binding.URL)
			reusable = false
		}
	}

	var browserTab browser.Tab
	if reusable {
		navigateRequest := browser.NavigateRequest{
			OperationID: request.OperationID, TabID: binding.BrowserTabID,
			URL: prepared.url, Action: browser.NavigateURL,
		}
		if request.UserInitiated {
			if renderer, ok := exec.(interface {
				navigateFilePreview(context.Context, browser.NavigateRequest) (browser.Tab, error)
			}); ok {
				browserTab, err = renderer.navigateFilePreview(ctx, navigateRequest)
			} else {
				browserTab, err = exec.Navigate(ctx, navigateRequest)
			}
		} else {
			browserTab, err = exec.Navigate(ctx, navigateRequest)
		}
	} else {
		browserTab, err = exec.Open(ctx, browser.OpenRequest{OperationID: request.OperationID, URL: prepared.url})
	}
	if err != nil {
		a.fileBrowserPreviewMu.Unlock()
		for _, retired := range retiredURLs {
			a.revokeWorkspaceBrowserPreview(retired)
		}
		a.revokeWorkspaceBrowserPreview(prepared.url)
		return FileBrowserPreviewResult{}, err
	}
	if reusable && binding.URL != prepared.url {
		retiredURLs = append(retiredURLs, binding.URL)
	}
	a.fileBrowserPreviews[key] = fileBrowserPreviewBinding{BrowserTabID: browserTab.ID, URL: prepared.url, SessionGeneration: generation}
	a.fileBrowserPreviewMu.Unlock()
	for _, retired := range retiredURLs {
		a.revokeWorkspaceBrowserPreview(retired)
	}
	if _, _, currentErr := a.fileBrowserPreviewTab(tabID, generation); currentErr != nil {
		_ = exec.Close(context.Background(), browser.CloseRequest{OperationID: request.OperationID + "-stale-close", TabID: browserTab.ID})
		return FileBrowserPreviewResult{}, currentErr
	}
	status := "opened"
	errorText := browserTab.Error
	if browserTab.Loading {
		status = "loading"
	}
	if errorText != "" {
		status = "failed"
	}
	return FileBrowserPreviewResult{TabID: browserTab.ID, URL: prepared.url, Status: status, Error: errorText, SessionGeneration: generation}, nil
}

func (a *App) fileBrowserPreviewTab(tabID string, expected uint64) (*WorkspaceTab, uint64, error) {
	a.mu.RLock()
	tab := a.tabs[tabID]
	if tab == nil {
		a.mu.RUnlock()
		return nil, 0, errors.New("workspace tab is no longer available")
	}
	generation := tab.SessionGeneration
	a.mu.RUnlock()
	if expected != 0 && generation != expected {
		return nil, generation, errors.New("the session changed before the preview opened")
	}
	return tab, generation, nil
}

func (a *App) prepareFileBrowserPreview(tabID string, request FileBrowserPreviewRequest) (preparedFileBrowserPreview, error) {
	var preview FilePreview
	var identity string
	var err error
	switch request.Source {
	case "presented":
		preview = a.ReadPresentedFileForTab(tabID, request.ToolCallID, request.Path)
		identity, err = a.ResolvePresentedPathForTab(tabID, request.ToolCallID, request.Path)
	case "reference":
		preview = a.ReadReferenceFileForTab(tabID, request.Path)
		identity, err = a.ResolveReferencePathForTab(tabID, request.Path)
	default:
		preview = a.ReadFileForTab(tabID, request.Path)
		identity, err = a.ResolveWorkspacePathForTab(tabID, request.Path)
	}
	if err != nil {
		if preview.URL != "" {
			a.revokeWorkspaceMediaPath(preview.URL)
		}
		return preparedFileBrowserPreview{}, err
	}
	if preview.Err != "" {
		if preview.URL != "" {
			a.revokeWorkspaceMediaPath(preview.URL)
		}
		return preparedFileBrowserPreview{}, errors.New(preview.Err)
	}
	if preview.URL == "" {
		return preparedFileBrowserPreview{}, errors.New("this file type cannot be opened in the built-in browser")
	}
	origin, err := a.ensureWorkspacePreviewOrigin()
	if err != nil {
		a.revokeWorkspaceMediaPath(preview.URL)
		return preparedFileBrowserPreview{}, err
	}
	a.extendWorkspaceBrowserPreviewToken(preview.URL)
	return preparedFileBrowserPreview{url: origin + preview.URL, identity: identity}, nil
}

func (a *App) releaseFileBrowserPreviewTab(browserTabID string) {
	if browserTabID == "" {
		return
	}
	a.fileBrowserPreviewMu.Lock()
	var URLs []string
	for key, binding := range a.fileBrowserPreviews {
		if binding.BrowserTabID != browserTabID {
			continue
		}
		delete(a.fileBrowserPreviews, key)
		URLs = append(URLs, binding.URL)
	}
	a.fileBrowserPreviewMu.Unlock()
	for _, rawURL := range URLs {
		a.revokeWorkspaceBrowserPreview(rawURL)
	}
}

func (a *App) releaseFileBrowserPreviewURL(rawURL string) {
	if rawURL == "" {
		return
	}
	a.fileBrowserPreviewMu.Lock()
	for key, binding := range a.fileBrowserPreviews {
		if binding.URL == rawURL {
			delete(a.fileBrowserPreviews, key)
		}
	}
	a.fileBrowserPreviewMu.Unlock()
}

func (a *App) releaseFileBrowserPreviewsForTask(tabID string) {
	if tabID == "" {
		return
	}
	prefix := tabID + "\x00"
	a.fileBrowserPreviewMu.Lock()
	var URLs []string
	for key, binding := range a.fileBrowserPreviews {
		if strings.HasPrefix(key, prefix) {
			delete(a.fileBrowserPreviews, key)
			URLs = append(URLs, binding.URL)
		}
	}
	a.fileBrowserPreviewMu.Unlock()
	for _, rawURL := range URLs {
		a.revokeWorkspaceBrowserPreview(rawURL)
	}
}
