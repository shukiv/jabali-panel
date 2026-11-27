package reconciler

import (
	"context"
	"net/netip"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// ourAddrs lists the addresses this server answers on: its public IPv4/IPv6
// and every IP in its pool (a domain's Listen IP). A name resolving to any of
// them reaches this origin, so it is not fronted and an HTTP-01 challenge for
// it lands here. Comparing with the public address alone took a domain bound
// to a secondary pool IP for a CDN-fronted one: no :80→:443 redirect, and
// DNS-01 or a parked certificate instead of HTTP-01.
func (r *Reconciler) ourAddrs(ctx context.Context, srv *models.ServerSettings) []string {
	var out []string
	add := func(a string) {
		if a == "" {
			return
		}
		// DNS answers come back canonical; a pool row keeps what the admin typed.
		if p, err := netip.ParseAddr(a); err == nil {
			a = p.Unmap().String()
		}
		out = append(out, a)
	}
	if srv != nil {
		add(srv.PublicIPv4)
		add(srv.PublicIPv6)
	}
	if r.managedIPs != nil {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		rows, err := r.managedIPs.ListAll(lctx)
		cancel()
		if err == nil {
			for _, ip := range rows {
				add(ip.Address)
			}
		}
	}
	return out
}
