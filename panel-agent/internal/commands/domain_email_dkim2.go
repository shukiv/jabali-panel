package commands

// domain_email.dkim2_set — converge DKIM2 chain-of-custody signing for one
// mail domain (GH #648).
//
// DKIM2 (draft-ietf-dkim-dkim2-spec) reuses the DKIM1 DNS record format
// verbatim (draft-ietf-dkim-dkim2-dns: "DKIM2 reuses a subset of the DKIM
// textual representation including this version number for compatibility
// with existing key deployment"), so enabling it needs NO DNS change: the
// Dkim2 signature reuses the domain's existing Ed25519 key AND the existing
// `jabali` selector, and the already-published
// jabali._domainkey.<domain> TXT verifies both signature generations.
// DKIM1 keeps signing alongside as the fallback for legacy verifiers — the
// coexistence is by design in the draft.
//
// The signature object shape was probe-verified against Stalwart 0.16.15:
// Dkim2* variants accept only domainId/selector/privateKey/stage —
// `canonicalization`, `report` and `headers` are DKIM1-era knobs the DKIM2
// tagged-enum rejects with invalidPatch (the draft fixes canonicalization
// and the signed-header set).

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dkim"
)

const dkim2SignatureType = "Dkim2Ed25519Sha256"

type dkim2SetParams struct {
	DomainName string `json:"domain_name"`
	Enabled    bool   `json:"enabled"`
}

type dkim2SetResponse struct {
	Ok      bool `json:"ok"`
	Changed bool `json:"changed"`
}

// dkim2SignatureIDs returns the ids of every Dkim2Ed25519Sha256 signature
// bound to domainID. DkimSignature/get has no server-side filter for
// domainId, so this fetches the (small) full list and filters client-side.
func dkim2SignatureIDs(ctx context.Context, domainID string) ([]string, error) {
	var result jmapGetResult
	if err := jmapCall(ctx, "x:DkimSignature/get", map[string]any{}, &result); err != nil {
		return nil, err
	}
	var ids []string
	for _, raw := range result.List {
		var sig struct {
			ID       string `json:"id"`
			Type     string `json:"@type"`
			DomainID string `json:"domainId"`
		}
		if err := json.Unmarshal(raw, &sig); err != nil {
			continue
		}
		if sig.Type == dkim2SignatureType && sig.DomainID == domainID {
			ids = append(ids, sig.ID)
		}
	}
	return ids, nil
}

func dkim2SetHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p dkim2SetParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("parse: %v", err),
		}
	}
	if !domainRegex.MatchString(p.DomainName) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid domain_name %q", p.DomainName),
		}
	}

	domainID, err := domainIDByName(ctx, p.DomainName)
	if err != nil {
		return nil, err
	}
	if domainID == "" {
		if !p.Enabled {
			// Nothing to remove for a domain Stalwart doesn't know.
			return dkim2SetResponse{Ok: true, Changed: false}, nil
		}
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("domain %q has no Stalwart registry entry — is email enabled?", p.DomainName),
		}
	}

	existing, err := dkim2SignatureIDs(ctx, domainID)
	if err != nil {
		return nil, err
	}

	if p.Enabled {
		if len(existing) > 0 {
			return dkim2SetResponse{Ok: true, Changed: false}, nil
		}
		keyPath := filepath.Join(dkimKeyDirFunc(), p.DomainName+".key")
		seed, err := dkim.LoadEd25519(keyPath)
		if err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("load dkim key %s: %v", keyPath, err),
			}
		}
		pemKey, err := dkim.SeedToPKCS8PEM(seed)
		if err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("convert dkim key: %v", err),
			}
		}
		if err := createDkim2Signature(ctx, domainID, dkimSelector, string(pemKey)); err != nil {
			return nil, err
		}
		return dkim2SetResponse{Ok: true, Changed: true}, nil
	}

	if len(existing) == 0 {
		return dkim2SetResponse{Ok: true, Changed: false}, nil
	}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:DkimSignature/set", map[string]any{"destroy": existing}, &result); err != nil {
		return nil, err
	}
	if len(result.Destroyed) != len(existing) {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart destroyed %d of %d dkim2 signatures", len(result.Destroyed), len(existing)),
		}
	}
	return dkim2SetResponse{Ok: true, Changed: true}, nil
}

// createDkim2Signature creates the minimal probe-verified Dkim2 object.
// Deliberately NOT merged with createDkimSignature: the DKIM1 creator's
// canonicalization/headers/report fields are invalid on the Dkim2 variant.
func createDkim2Signature(ctx context.Context, domainID, selector, pemPrivateKey string) error {
	args := map[string]any{
		"create": map[string]any{
			"#sig2": map[string]any{
				"@type":    dkim2SignatureType,
				"domainId": domainID,
				"selector": selector,
				"privateKey": map[string]any{
					"@type":  "Text",
					"secret": pemPrivateKey,
				},
				"stage": "active",
			},
		},
	}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:DkimSignature/set", args, &result); err != nil {
		return err
	}
	if _, ok := result.Created["#sig2"]; ok {
		return nil
	}
	if reason, nok := result.NotCreated["#sig2"]; nok {
		return &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("stalwart x:DkimSignature/set dkim2 create refused: %s", string(reason)),
		}
	}
	return &agentwire.AgentError{
		Code:    agentwire.CodeInternal,
		Message: "stalwart x:DkimSignature/set: unexpected response shape",
	}
}

func init() {
	Default.Register("domain_email.dkim2_set", dkim2SetHandler)
}
