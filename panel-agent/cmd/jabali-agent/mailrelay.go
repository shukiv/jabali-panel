package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-agent/internal/mailrelay"
)

// runMailRelay is `jabali-agent mailrelay`: the relay that sends the sites'
// PHP mail() through the operator's smarthost (GH #2056, ADR 0174). It holds
// the smarthost login, so it never runs as root and never as the agent:
// jabali-mailrelay.service starts it as the jabali-mailrelay user.
func runMailRelay(args []string) int {
	fs := flag.NewFlagSet("mailrelay", flag.ContinueOnError)
	socket := fs.String("socket", mailrelay.SocketPath, "unix socket the jabali-sendmail shim connects to")
	config := fs.String("config", mailrelay.ConfigPath, "relay config written by the agent (smarthost + senders)")
	logFormat := fs.String("log-format", envOr("JABALI_AGENT_LOG_FORMAT", "json"), "json|text")
	if err := fs.Parse(args); err != nil {
		return 64
	}
	log := newLogger(*logFormat, "info")

	if os.Geteuid() == 0 {
		log.Error("the mail relay must not run as root; start it with jabali-mailrelay.service")
		return 1
	}

	ln, err := mailrelay.Listen(*socket)
	if err != nil {
		log.Error("mail relay listen failed", "socket", *socket, "err", err)
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	log.Info("mail relay listening", "socket", *socket)
	srv := &mailrelay.Server{ConfigPath: *config, Log: log}
	if err := srv.Serve(ctx, ln); err != nil {
		log.Error("mail relay stopped", "err", err)
		return 1
	}
	log.Info("mail relay stopped")
	return 0
}
