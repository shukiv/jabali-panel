package api

import (
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"github.com/gin-gonic/gin"
)

// M65RouteDeps holds every repository + client any M6.5 feature needs.
// One struct for all sub-routes so parallel feature steps don't collide
// on the registration signature.
type M65RouteDeps struct {
	Agent           agent.AgentInterface
	Domains         repository.DomainRepository
	Mailboxes       repository.MailboxRepository
	Autoresponders  repository.EmailAutoresponderRepository
	Forwarders      repository.EmailForwarderRepository
	MailboxShares   repository.MailboxShareRepository
	SharedResources repository.SharedResourceRepository
	SendDelegations repository.MailboxSendDelegationRepository
}

// RegisterM65Routes registers all M6.5 email feature routes.
// Called once from app.go; each sub-registration lives in its own Wave file,
// preventing file collisions. Enables parallel Wave B/C development (ADR-0051).
func RegisterM65Routes(g *gin.RouterGroup, deps M65RouteDeps) {
	registerForwarderRoutes(g, deps)
	registerAutoresponderRoutes(g, deps)
	registerCatchAllRoutes(g, deps)
	registerDisclaimerRoutes(g, deps)
	registerSharedFolderRoutes(g, deps)
	registerMailLogRoutes(g, deps)
	registerSharedResourceRoutes(g, deps)
	registerSendAsRoutes(g, deps)
}

// GH #347: send-as delegation.
func registerSendAsRoutes(g *gin.RouterGroup, deps M65RouteDeps) {
	RegisterMailboxSendAsRoutes(g, MailboxSendAsHandlerConfig{
		Mailboxes:       deps.Mailboxes,
		Domains:         deps.Domains,
		SendDelegations: deps.SendDelegations,
		Agent:           deps.Agent,
	})
}

// Wave B: forwarders.
func registerForwarderRoutes(g *gin.RouterGroup, deps M65RouteDeps) {
	RegisterMailboxForwarderRoutes(g, MailboxForwarderHandlerConfig{
		Mailboxes:  deps.Mailboxes,
		Domains:    deps.Domains,
		Forwarders: deps.Forwarders,
		Agent:      deps.Agent,
	})
}

// Wave B: autoresponders.
func registerAutoresponderRoutes(g *gin.RouterGroup, deps M65RouteDeps) {
	RegisterMailboxAutoresponderRoutes(g, MailboxAutoresponderHandlerConfig{
		Mailboxes:      deps.Mailboxes,
		Domains:        deps.Domains,
		Autoresponders: deps.Autoresponders,
		Agent:          deps.Agent,
	})
}

// Wave B: catch-all.
func registerCatchAllRoutes(g *gin.RouterGroup, deps M65RouteDeps) {
	RegisterDomainCatchallRoutes(g, DomainCatchallHandlerConfig{
		Agent:   deps.Agent,
		Domains: deps.Domains,
	})
}

// Wave C: disclaimer.
func registerDisclaimerRoutes(g *gin.RouterGroup, deps M65RouteDeps) {
	RegisterDomainDisclaimerRoutes(g, DomainDisclaimerHandlerConfig{
		Domains: deps.Domains,
		Agent:   deps.Agent,
	})
}

// Wave B: shared folders.
func registerSharedFolderRoutes(g *gin.RouterGroup, deps M65RouteDeps) {
	RegisterMailboxShareRoutes(g, MailboxShareHandlerConfig{
		Mailboxes:     deps.Mailboxes,
		Domains:       deps.Domains,
		MailboxShares: deps.MailboxShares,
		Agent:         deps.Agent,
	})
}

// Wave C: mail logs.
func registerMailLogRoutes(g *gin.RouterGroup, deps M65RouteDeps) {
	RegisterMailLogsRoutes(g, MailLogsHandlerConfig{
		Domains: deps.Domains,
		Agent:   deps.Agent,
	})
}

// M52 (ADR-0133): standalone shared resources + grants.
func registerSharedResourceRoutes(g *gin.RouterGroup, deps M65RouteDeps) {
	RegisterSharedResourceRoutes(g, SharedResourceHandlerConfig{
		Resources: deps.SharedResources,
		Domains:   deps.Domains,
		Agent:     deps.Agent,
	})
}
