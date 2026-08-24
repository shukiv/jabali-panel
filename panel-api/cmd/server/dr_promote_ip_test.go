package main

import (
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func TestRepointPlan(t *testing.T) {
	def := func(id uint64, addr string) *models.ManagedIP {
		return &models.ManagedIP{ID: id, Address: addr, Family: "ipv4", IsDefault: true}
	}
	tests := []struct {
		name          string
		ownIP         string
		defaultRow    *models.ManagedIP
		ownIPRow      *models.ManagedIP
		publicIPv4    string
		wantChange    bool
		wantCollision bool
	}{
		{
			name:       "already default and public → no-op",
			ownIP:      "203.0.113.9",
			defaultRow: def(1, "203.0.113.9"),
			publicIPv4: "203.0.113.9",
			wantChange: false,
		},
		{
			name:       "default address is old primary → re-point in place",
			ownIP:      "203.0.113.9",
			defaultRow: def(1, "198.51.100.7"),
			publicIPv4: "198.51.100.7",
			wantChange: true,
		},
		{
			name:       "default matches but public_ipv4 stale → still a change",
			ownIP:      "203.0.113.9",
			defaultRow: def(1, "203.0.113.9"),
			publicIPv4: "198.51.100.7",
			wantChange: true,
		},
		{
			name:          "own IP already a different managed row → collision, refuse",
			ownIP:         "203.0.113.9",
			defaultRow:    def(1, "198.51.100.7"),
			ownIPRow:      &models.ManagedIP{ID: 2, Address: "203.0.113.9", Family: "ipv4"},
			publicIPv4:    "198.51.100.7",
			wantChange:    false,
			wantCollision: true,
		},
		{
			name:       "own IP row IS the default row → in-place, not a collision",
			ownIP:      "203.0.113.9",
			defaultRow: def(1, "203.0.113.9"),
			ownIPRow:   def(1, "203.0.113.9"),
			publicIPv4: "198.51.100.7",
			wantChange: true,
		},
		{
			name:       "no default row yet → seed (change)",
			ownIP:      "203.0.113.9",
			defaultRow: nil,
			publicIPv4: "",
			wantChange: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &models.ServerSettings{PublicIPv4: tt.publicIPv4}
			p := repointPlan(tt.ownIP, tt.defaultRow, tt.ownIPRow, s)
			if p.Change != tt.wantChange {
				t.Errorf("Change = %v, want %v", p.Change, tt.wantChange)
			}
			if p.Collision != tt.wantCollision {
				t.Errorf("Collision = %v, want %v", p.Collision, tt.wantCollision)
			}
			if p.OwnIP != tt.ownIP {
				t.Errorf("OwnIP = %q, want %q", p.OwnIP, tt.ownIP)
			}
		})
	}
}
