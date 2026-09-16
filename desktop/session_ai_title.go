package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"reasonix/internal/agent"
	"reasonix/internal/control"
	"reasonix/internal/provider"
	"reasonix/internal/session"
	"reasonix/internal/sessioncatalog"
)

const (
	aiSessionTitleMaxTurns     = 3
	aiSessionTitleMaxTurnRunes = 500
)

// AIRenameSession generates a title from an open topic's own conversation and
// applies it as an explicit title on that physical session. Binding generation
// and persistence to the same controller/path prevents a tab switch from
// routing a delayed provider response into the newly active conversation.
func (a *App) AIRenameSession(topicID string) (string, error) {
	topicID = strings.TrimSpace(topicID)
	if topicID == "" {
		return "", fmt.Errorf("empty topic id")
	}
	ctrl := a.controllerForTopic(topicID)
	if ctrl == nil {
		return "", fmt.Errorf("session is not open; open it before using AI rename")
	}
	if ctrl.UsesExclusiveSession() {
		return a.aiRenameCanonicalSession(topicID, ctrl)
	}
	sessionDir := ctrl.SessionDir()
	sessionPath := strings.TrimSpace(ctrl.SessionPath())
	if sessionPath == "" {
		if scope, workspaceRoot, ok := a.findTopicLocation(topicID); ok {
			sessionPath = a.catalogSessionPathForTopic(scope, workspaceRoot, topicID)
		}
	}
	validated, _, err := validateSessionPath(sessionDir, sessionPath)
	if err != nil {
		return "", fmt.Errorf("AI rename session: %w", err)
	}
	expectedTitle := ""
	if meta, ok, loadErr := agent.LoadBranchMeta(validated); loadErr != nil {
		return "", fmt.Errorf("AI rename session: read current title: %w", loadErr)
	} else if ok {
		expectedTitle = meta.CustomTitle
	}
	users, err := loadTopicTitleUserTurnsFromSession(validated)
	if err != nil {
		return "", fmt.Errorf("AI rename session: read conversation: %w", err)
	}
	if len(users) == 0 {
		return "", fmt.Errorf("session has no user messages to analyze")
	}
	title, err := ctrl.GenerateSessionTitle(a.bootContext(), sessionTitleTranscript(users))
	if err != nil {
		return "", err
	}
	if !a.topicControllerOwnsSession(topicID, ctrl, validated) {
		return "", fmt.Errorf("session changed while AI rename was running; try again")
	}
	if err := a.renameSessionInDirIfTitleUnchanged(sessionDir, validated, expectedTitle, title); err != nil {
		if errors.Is(err, agent.ErrSessionTitleChanged) {
			return "", fmt.Errorf("session title changed while AI rename was running; try again")
		}
		return "", err
	}
	return title, nil
}

func (a *App) aiRenameCanonicalSession(topicID string, ctrl *control.Controller) (string, error) {
	service, runtime, bound := ctrl.SessionBinding()
	if !bound {
		return "", fmt.Errorf("session has no user messages to analyze")
	}
	ref := runtime.Ref()
	var topicRoot string
	var hasTopic bool
	a.mu.RLock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab != nil && tab.Ctrl == ctrl && tab.TopicID == topicID {
			topicRoot, hasTopic = topicTitleRoot(tab.Scope, tab.WorkspaceRoot), true
			break
		}
	}
	a.mu.RUnlock()
	expectedTopicTitle := ""
	if hasTopic {
		expectedTopicTitle = loadTopicTitle(topicRoot, topicID)
	}
	ctx, cancel := context.WithTimeout(a.bootContext(), 30*time.Second)
	defer cancel()
	info, err := service.Query().Stat(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("AI rename session: read current title: %w", err)
	}
	// Publish accepted user turns before querying the durable history index.
	if _, err := runtime.Session().Flush(ctx); err != nil {
		return "", fmt.Errorf("AI rename session: flush conversation: %w", err)
	}
	messages, err := service.Query().TitleMessages(ctx, ref, aiSessionTitleMaxTurns)
	if err != nil {
		return "", fmt.Errorf("AI rename session: read conversation: %w", err)
	}
	var users []string
	for _, message := range messages {
		if content := topicTitleUserText(message); content != "" {
			users = append(users, content)
		}
	}
	if len(users) == 0 {
		return "", fmt.Errorf("session has no user messages to analyze")
	}
	title, err := ctrl.GenerateSessionTitle(ctx, sessionTitleTranscript(users))
	if err != nil {
		return "", err
	}
	// The legacy sidebar still owns a topic label. Serialize its projection
	// with manual/automatic topic renames, as well as guarding the session title.
	a.topicTitleMutationMu.Lock()
	defer a.topicTitleMutationMu.Unlock()
	currentRef, stillBound := ctrl.SessionRef()
	if a.controllerForTopic(topicID) != ctrl || !stillBound || currentRef != ref {
		return "", fmt.Errorf("session changed while AI rename was running; try again")
	}
	if hasTopic && loadTopicTitle(topicRoot, topicID) != expectedTopicTitle {
		return "", fmt.Errorf("session title changed while AI rename was running; try again")
	}
	if err := service.SetTitleIfUnchanged(ctx, ref, info.Title, title); err != nil {
		if errors.Is(err, session.ErrSessionTitleChanged) {
			return "", fmt.Errorf("session title changed while AI rename was running; try again")
		}
		return "", err
	}
	if hasTopic {
		if err := setTopicTitle(topicRoot, topicID, title); err != nil {
			return "", fmt.Errorf("AI rename session: update topic title: %w", err)
		}
		a.updateOpenTopicTitle(topicID, title, topicTitleSourceManual)
	}
	a.invalidatePromptHistoryCache()
	a.emitProjectTreeChanged()
	return title, nil
}

func (a *App) controllerForTopic(topicID string) *control.Controller {
	a.mu.RLock()
	defer a.mu.RUnlock()
	var found *control.Controller
	for _, tab := range a.runtimeTabsLocked() {
		if tab == nil || (strings.TrimSpace(tab.TopicID) != topicID && tab.SessionID != topicID && sessionRoute(tab.SessionID) != topicID) || tab.Ctrl == nil {
			continue
		}
		if ctrl, ok := tab.Ctrl.(*control.Controller); ok {
			if tab.ID == a.activeTabID {
				return ctrl
			}
			if found != nil && found != ctrl {
				return nil
			}
			found = ctrl
		}
	}
	return found
}

func (a *App) topicControllerOwnsSession(topicID string, ctrl *control.Controller, sessionPath string) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, tab := range a.runtimeTabsLocked() {
		if tab == nil || tab.TopicID != topicID || tab.Ctrl != ctrl {
			continue
		}
		return sessionRuntimeKey(tab.currentSessionPath()) == sessionRuntimeKey(sessionPath)
	}
	return false
}

func topicTitleUserText(message provider.Message) string {
	if !agent.IsUserAuthoredTurnMessage(message) {
		return ""
	}
	content := control.StripComposePrefixes(agent.UserPreviewText(agent.UserMessageText(message)))
	return strings.TrimSpace(control.StripReferencedContextPrefix(content))
}

func sessionTitleTranscript(users []string) string {
	parts := make([]string, 0, aiSessionTitleMaxTurns)
	for _, user := range users {
		if len(parts) >= aiSessionTitleMaxTurns {
			break
		}
		user = strings.TrimSpace(user)
		if runes := []rune(user); len(runes) > aiSessionTitleMaxTurnRunes {
			user = string(runes[:aiSessionTitleMaxTurnRunes])
		}
		if user != "" {
			parts = append(parts, user)
		}
	}
	return strings.Join(parts, "\n\n")
}

func sessionPreviewForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if meta, ok, err := agent.LoadBranchMeta(path); err == nil && ok {
		if preview := strings.TrimSpace(meta.Preview); preview != "" {
			return preview
		}
	}
	preview, ok, err := agent.LoadSessionPreviewFromDisplayIndex(path)
	if err != nil || !ok {
		return ""
	}
	return preview
}

func topicSessionPreview(sessions []sessioncatalog.SessionRecord, path string) string {
	for _, session := range sessions {
		if sessionRuntimeKey(session.Path) == sessionRuntimeKey(path) {
			return strings.TrimSpace(session.Preview)
		}
	}
	return ""
}
