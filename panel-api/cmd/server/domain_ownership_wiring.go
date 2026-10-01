package main

import (
	"log/slog"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/app"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ownershipops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// newOwnershipService builds the GH #1816 / ADR-0170 ownership service the
// API and the ticker share. It returns nil without a database or a domain
// repository.
//
// Optional dependencies are assigned only when present: a nil pointer stored
// in an interface field is a non-nil interface that panics on first use.
func newOwnershipService(deps app.Deps, sharedAgent *agent.Client, log *slog.Logger) *ownershipops.Service {
	if deps.DB == nil || deps.Domains == nil {
		return nil
	}
	d := ownershipops.Deps{
		Store:     repository.NewDomainOwnershipRepository(deps.DB),
		Domains:   deps.Domains,
		Teardowns: deps.DomainTeardowns,
		Ports:     repository.NewPortAllocationRepository(deps.DB),
		Log:       log,
	}
	if deps.WebDomainAliases != nil {
		d.Aliases = deps.WebDomainAliases
	}
	if deps.ServerSettings != nil {
		d.Settings = deps.ServerSettings
	}
	if deps.Reconciler != nil {
		rec := deps.Reconciler
		d.Schedule = func(id string) { rec.Schedule(id) }
	}
	if deps.NotificationQueue != nil {
		d.Notify = deps.NotificationQueue
	}
	d.Audit = deps.AuditRecorder // an interface already: nil stays nil
	if sharedAgent != nil {
		d.Agent = sharedAgent
	}
	return ownershipops.New(d)
}
