package main

import (
	"context"
	"log/slog"

	"reasonix/internal/session"
)

// Run after controllers and lifecycle barriers release their bindings.
func (a *App) closeSessionServices() {
	a.desktopSessions.readSnapshots.close()
	a.sessionServicesMu.Lock()
	services := make([]*session.Service, 0, len(a.sessionServices))
	for _, service := range a.sessionServices {
		services = append(services, service)
	}
	a.sessionServicesMu.Unlock()
	for _, service := range services {
		if err := service.Shutdown(context.Background()); err != nil {
			slog.Warn("desktop: close session service", "err", err)
		}
	}
}
