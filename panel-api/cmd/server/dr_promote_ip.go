package main

import (
	"context"
	"fmt"
	"net"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// GH #1169 gap 1 — IP re-point at promote (cutover-critical).
//
// managed_ips + server_settings.public_ipv4 replicate from the OLD primary, so
// after a promote every rebuilt vhost binds an address this box does not own —
// nothing serves until the operator hand-edits the DB. Promote re-points the
// default IPv4 managed IP (and public_ipv4) to this box's own address so the
// reconciler builds bindable vhosts on its first tick.

// detectOwnIPv4 returns the box's own egress IPv4. Injectable so the re-point
// logic is unit-testable without a real interface. The default "connects" a UDP
// socket to a public address — no packet is sent, it only makes the kernel
// resolve the egress source IP for the default route, so it needs a route but
// not reachability.
var detectOwnIPv4 = defaultDetectOwnIPv4

func defaultDetectOwnIPv4(ctx context.Context) (string, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", "1.1.1.1:80")
	if err != nil {
		return "", fmt.Errorf("detect own IPv4 (no default route?): %w", err)
	}
	defer conn.Close()
	ua, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || ua.IP == nil {
		return "", fmt.Errorf("detect own IPv4: unexpected local address %v", conn.LocalAddr())
	}
	ip := ua.IP.To4()
	if ip == nil {
		return "", fmt.Errorf("detect own IPv4: egress address %s is not IPv4", ua.IP)
	}
	return ip.String(), nil
}

// ipRepointPlan is the pure decision for the IP re-point. Separated from I/O so
// the branch logic (no-op / in-place update / collision) is unit-tested.
type ipRepointPlan struct {
	Change        bool   // false → public_ipv4 and the default row already match ownIP
	OwnIP         string // this box's detected IPv4
	OldDefaultIP  string // the default managed IP before the re-point ("" if none)
	OldPublicIPv4 string
	// Collision is set when ownIP already exists as a DIFFERENT managed_ips row
	// (address has a unique index). We refuse rather than half-repoint — the
	// operator resolves it, since flipping is_default there would also strand
	// domains whose listen_ipv4_id points at the old default row.
	Collision bool
}

// repointPlan computes what the re-point should do. defaultRow is the current
// is_default IPv4 row (nil if none); ownIPRow is the row already holding ownIP
// (nil if none) — used only to detect the unique-index collision.
func repointPlan(ownIP string, defaultRow, ownIPRow *models.ManagedIP, settings *models.ServerSettings) ipRepointPlan {
	p := ipRepointPlan{OwnIP: ownIP}
	if settings != nil {
		p.OldPublicIPv4 = settings.PublicIPv4
	}
	if defaultRow != nil {
		p.OldDefaultIP = defaultRow.Address
	}
	alreadyDefault := defaultRow != nil && defaultRow.Address == ownIP
	alreadyPublic := settings != nil && settings.PublicIPv4 == ownIP
	if alreadyDefault && alreadyPublic {
		return p // no-op
	}
	// A row already holding ownIP that is NOT the default row we'd update in
	// place = unique-index collision.
	if ownIPRow != nil && (defaultRow == nil || ownIPRow.ID != defaultRow.ID) {
		p.Collision = true
		return p
	}
	p.Change = true
	return p
}

// repointDefaultIP applies the re-point: it updates the default IPv4 managed_ips
// row's address in place (preserving its id so domains' listen_ipv4_id
// references stay valid, M24) and sets settings.PublicIPv4. The caller persists
// settings (the role-flip Upsert already does). Returns a human summary line,
// or an error the promote surfaces without flipping.
func repointDefaultIP(ctx context.Context, ips repository.ManagedIPRepository, settings *models.ServerSettings, ownIP string) (string, error) {
	defaultRow, derr := ips.FindDefaultByFamily(ctx, "ipv4")
	if derr != nil {
		return "", fmt.Errorf("read default IPv4 managed IP: %w", derr)
	}
	ownIPRow, aerr := ips.FindByAddress(ctx, ownIP)
	if aerr != nil {
		return "", fmt.Errorf("look up managed IP %s: %w", ownIP, aerr)
	}
	plan := repointPlan(ownIP, defaultRow, ownIPRow, settings)
	if plan.Collision {
		return "", fmt.Errorf("this box's IP %s is already a non-default managed IP; re-point manually "+
			"(set it default and repoint domains) — refusing to half-repoint", ownIP)
	}
	if !plan.Change {
		return fmt.Sprintf("default IPv4 already %s — no re-point needed", ownIP), nil
	}
	if defaultRow != nil {
		defaultRow.Address = ownIP
		// The box's own primary IP is bound by the OS, not the agent's
		// ip-addr-add loop; mark bound so the rebind loop leaves it alone.
		defaultRow.IsBound = true
		defaultRow.Degraded = false
		if uerr := ips.Update(ctx, defaultRow); uerr != nil {
			return "", fmt.Errorf("re-point default IPv4 managed IP to %s: %w", ownIP, uerr)
		}
	} else if eerr := ips.EnsureDefault(ctx, ownIP, "ipv4"); eerr != nil {
		return "", fmt.Errorf("seed default IPv4 managed IP %s: %w", ownIP, eerr)
	}
	settings.PublicIPv4 = ownIP
	return fmt.Sprintf("re-pointed default IPv4 %s → %s (public_ipv4 updated)", orDefault(plan.OldDefaultIP, "none"), ownIP), nil
}
