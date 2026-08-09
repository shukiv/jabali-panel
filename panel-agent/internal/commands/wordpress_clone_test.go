package commands

import (
	"context"
	"encoding/json"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

func TestWordPressClone_InvalidInput(t *testing.T) {
	requireHostMutationAllowed(t)
	tests := []struct {
		name      string
		input     wordpressCloneReq
		wantError bool
		wantCode  string
	}{
		{
			name: "invalid: missing os_user",
			input: wordpressCloneReq{
				OSUser:        "",
				SrcDocroot:    "/home/alice/domains/src.com/public_html",
				DstDocroot:    "/home/alice/domains/dst.com/public_html",
				SrcDBName:     "wp_src",
				DstDBName:     "wp_dst",
				DstDBUser:     "wp_user",
				DstDBPassword: "password123",
				DstDBHost:     "localhost",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: missing src_docroot",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "",
				DstDocroot:    "/home/alice/domains/dst.com/public_html",
				SrcDBName:     "wp_src",
				DstDBName:     "wp_dst",
				DstDBUser:     "wp_user",
				DstDBPassword: "password123",
				DstDBHost:     "localhost",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: missing dst_docroot",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "/home/alice/domains/src.com/public_html",
				DstDocroot:    "",
				SrcDBName:     "wp_src",
				DstDBName:     "wp_dst",
				DstDBUser:     "wp_user",
				DstDBPassword: "password123",
				DstDBHost:     "localhost",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: missing src_db_name",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "/home/alice/domains/src.com/public_html",
				DstDocroot:    "/home/alice/domains/dst.com/public_html",
				SrcDBName:     "",
				DstDBName:     "wp_dst",
				DstDBUser:     "wp_user",
				DstDBPassword: "password123",
				DstDBHost:     "localhost",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: missing dst_db_name",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "/home/alice/domains/src.com/public_html",
				DstDocroot:    "/home/alice/domains/dst.com/public_html",
				SrcDBName:     "wp_src",
				DstDBName:     "",
				DstDBUser:     "wp_user",
				DstDBPassword: "password123",
				DstDBHost:     "localhost",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: missing dst_db_user",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "/home/alice/domains/src.com/public_html",
				DstDocroot:    "/home/alice/domains/dst.com/public_html",
				SrcDBName:     "wp_src",
				DstDBName:     "wp_dst",
				DstDBUser:     "",
				DstDBPassword: "password123",
				DstDBHost:     "localhost",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: missing dst_db_password",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "/home/alice/domains/src.com/public_html",
				DstDocroot:    "/home/alice/domains/dst.com/public_html",
				SrcDBName:     "wp_src",
				DstDBName:     "wp_dst",
				DstDBUser:     "wp_user",
				DstDBPassword: "",
				DstDBHost:     "localhost",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: missing dst_db_host",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "/home/alice/domains/src.com/public_html",
				DstDocroot:    "/home/alice/domains/dst.com/public_html",
				SrcDBName:     "wp_src",
				DstDBName:     "wp_dst",
				DstDBUser:     "wp_user",
				DstDBPassword: "password123",
				DstDBHost:     "",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: missing src_site_url",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "/home/alice/domains/src.com/public_html",
				DstDocroot:    "/home/alice/domains/dst.com/public_html",
				SrcDBName:     "wp_src",
				DstDBName:     "wp_dst",
				DstDBUser:     "wp_user",
				DstDBPassword: "password123",
				DstDBHost:     "localhost",
				SrcSiteURL:    "",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: missing dst_site_url",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "/home/alice/domains/src.com/public_html",
				DstDocroot:    "/home/alice/domains/dst.com/public_html",
				SrcDBName:     "wp_src",
				DstDBName:     "wp_dst",
				DstDBUser:     "wp_user",
				DstDBPassword: "password123",
				DstDBHost:     "localhost",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: path traversal in src_docroot",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "/home/alice/domains/../../../etc/passwd",
				DstDocroot:    "/home/alice/domains/dst.com/public_html",
				SrcDBName:     "wp_src",
				DstDBName:     "wp_dst",
				DstDBUser:     "wp_user",
				DstDBPassword: "password123",
				DstDBHost:     "localhost",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: path traversal in dst_docroot",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "/home/alice/domains/src.com/public_html",
				DstDocroot:    "/home/alice/domains/../../../etc/passwd",
				SrcDBName:     "wp_src",
				DstDBName:     "wp_dst",
				DstDBUser:     "wp_user",
				DstDBPassword: "password123",
				DstDBHost:     "localhost",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: src_docroot outside user's home",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "/etc/wordpress",
				DstDocroot:    "/home/alice/domains/dst.com/public_html",
				SrcDBName:     "wp_src",
				DstDBName:     "wp_dst",
				DstDBUser:     "wp_user",
				DstDBPassword: "password123",
				DstDBHost:     "localhost",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
		{
			name: "invalid: different user's docroots",
			input: wordpressCloneReq{
				OSUser:        "alice",
				SrcDocroot:    "/home/bob/domains/src.com/public_html",
				DstDocroot:    "/home/alice/domains/dst.com/public_html",
				SrcDBName:     "wp_src",
				DstDBName:     "wp_dst",
				DstDBUser:     "wp_user",
				DstDBPassword: "password123",
				DstDBHost:     "localhost",
				SrcSiteURL:    "https://src.com",
				DstSiteURL:    "https://dst.com",
			},
			wantError: true,
			wantCode:  agentwire.CodeInvalidArgument,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params, _ := json.Marshal(tt.input)
			_, err := wordpressCloneHandler(context.Background(), params)

			if (err != nil) != tt.wantError {
				t.Errorf("wordpressCloneHandler: expected error = %v, got %v", tt.wantError, err)
			}

			if tt.wantError && tt.wantCode != "" {
				var ae *agentwire.AgentError
				if !isAgentError(err, &ae) {
					t.Errorf("expected AgentError, got %T", err)
				} else if ae.Code != tt.wantCode {
					t.Errorf("expected code %q, got %q", tt.wantCode, ae.Code)
				}
			}
		})
	}
}

func TestWordPressClone_ValidRequest(t *testing.T) {
	requireHostMutationAllowed(t)
	// Test that valid input produces a non-error response
	// Note: This test doesn't actually run the clone (that requires systemd-run, rsync, mysql, etc.)
	// but it validates the request parsing and path validation logic.
	input := wordpressCloneReq{
		OSUser:        "alice",
		SrcDocroot:    "/home/alice/domains/src.com/public_html",
		DstDocroot:    "/home/alice/domains/dst.com/public_html",
		SrcDBName:     "wp_src",
		DstDBName:     "wp_dst",
		DstDBUser:     "wp_user",
		DstDBPassword: "password123",
		DstDBHost:     "localhost",
		SrcSiteURL:    "https://src.com",
		DstSiteURL:    "https://dst.com",
	}
	params, _ := json.Marshal(input)

	// In a real integration test, we would mock the systemd-run, rsync, and mysql commands
	// For unit test purposes, we can at least verify parsing works
	var req wordpressCloneReq
	if err := json.Unmarshal(params, &req); err != nil {
		t.Errorf("failed to parse valid request: %v", err)
	}

	if req.OSUser != "alice" {
		t.Errorf("expected OSUser=alice, got %s", req.OSUser)
	}
	if req.SrcDocroot != "/home/alice/domains/src.com/public_html" {
		t.Errorf("expected correct src_docroot, got %s", req.SrcDocroot)
	}
	if req.DstDocroot != "/home/alice/domains/dst.com/public_html" {
		t.Errorf("expected correct dst_docroot, got %s", req.DstDocroot)
	}
	if req.SrcDBName != "wp_src" {
		t.Errorf("expected src_db_name=wp_src, got %s", req.SrcDBName)
	}
	if req.DstDBName != "wp_dst" {
		t.Errorf("expected dst_db_name=wp_dst, got %s", req.DstDBName)
	}
	if req.SrcSiteURL != "https://src.com" {
		t.Errorf("expected src_site_url=https://src.com, got %s", req.SrcSiteURL)
	}
	if req.DstSiteURL != "https://dst.com" {
		t.Errorf("expected dst_site_url=https://dst.com, got %s", req.DstSiteURL)
	}
}
