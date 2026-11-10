package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dkim"
)

// Every mail domain carries two DKIM1 signatures (GH #2475): Ed25519 on
// dkimSelector and RSA on dkimRSASelector. Ed25519 alone left mail with no
// usable DKIM signature at receivers that don't verify it, so it failed DKIM
// alignment there. Stalwart signs each message with every DKIM1 signature of
// the sender's domain, so two signatures give two DKIM-Signature headers.
//
// Stalwart's registry holds the key it signs with; the files under
// dkimKeyDir are the panel's copy, from which the public TXT is derived. So:
//   - a signature that exists is kept as it is: a new key under a working
//     signature would publish a key nothing signs with;
//   - a missing signature is created from the key file, and the key is
//     generated only when the file is missing too;
//   - a signature whose key file is gone is kept, and its public key is
//     reported as "" so the panel keeps the one it published.
const (
	dkim1Ed25519Type = "Dkim1Ed25519Sha256"
	dkim1RSAType     = "Dkim1RsaSha256"
	dkimRSASelector  = dkim.RSASelector
)

func dkimRSAKeyPath(domainName string) string {
	return filepath.Join(dkimKeyDirFunc(), domainName+".rsa.key")
}

// dkimEnsureResult is what ensureDomainDKIM did for one domain.
type dkimEnsureResult struct {
	// Ed25519PublicKey and RSAPublicKey are the TXT values of the keys the
	// domain's signatures use; "" when a signature exists but its key file
	// can't be read.
	Ed25519PublicKey string
	RSAPublicKey     string
	// Created lists the signature types this call created.
	Created []string
}

// ensureDomainDKIM gives the Stalwart domain domainID an Ed25519 and an RSA
// DKIM1 signature, under the rules above.
func ensureDomainDKIM(ctx context.Context, domainName, domainID string) (dkimEnsureResult, error) {
	var res dkimEnsureResult

	edIDs, err := dkimSignatureIDs(ctx, domainID, dkim1Ed25519Type)
	if err != nil {
		return res, err
	}
	edPath := filepath.Join(dkimKeyDirFunc(), domainName+".key")
	if len(edIDs) > 0 {
		res.Ed25519PublicKey = existingKeyTXT(domainName, edPath, func() ([]byte, error) {
			seed, err := dkim.LoadEd25519(edPath)
			if err != nil {
				return nil, err
			}
			return dkim.PublicDKIMTxtFromSeed(seed)
		})
	} else {
		txt, err := ensureDKIMKey(edPath)
		if err != nil {
			return res, err
		}
		seed, err := dkim.LoadEd25519(edPath)
		if err != nil {
			return res, dkimInternal("load DKIM key %s: %v", edPath, err)
		}
		pemKey, err := dkim.SeedToPKCS8PEM(seed)
		if err != nil {
			return res, dkimInternal("wrap DKIM seed as PKCS#8 PEM: %v", err)
		}
		if err := createDkim1Signature(ctx, dkim1Ed25519Type, domainID, dkimSelector, string(pemKey)); err != nil {
			return res, err
		}
		res.Ed25519PublicKey = txt
		res.Created = append(res.Created, dkim1Ed25519Type)
	}

	rsaIDs, err := dkimSignatureIDs(ctx, domainID, dkim1RSAType)
	if err != nil {
		return res, err
	}
	rsaPath := dkimRSAKeyPath(domainName)
	if len(rsaIDs) > 0 {
		res.RSAPublicKey = existingKeyTXT(domainName, rsaPath, func() ([]byte, error) {
			pemKey, err := dkim.LoadRSA(rsaPath)
			if err != nil {
				return nil, err
			}
			return dkim.PublicRSADKIMTxtFromPEM(pemKey)
		})
	} else {
		pemKey, txt, err := ensureDKIMRSAKey(rsaPath)
		if err != nil {
			return res, err
		}
		if err := createDkim1Signature(ctx, dkim1RSAType, domainID, dkimRSASelector, string(pemKey)); err != nil {
			return res, err
		}
		res.RSAPublicKey = txt
		res.Created = append(res.Created, dkim1RSAType)
	}
	return res, nil
}

// existingKeyTXT derives the TXT of the key a signature already uses, or
// returns "" (logged) when its file can't be read.
func existingKeyTXT(domainName, path string, derive func() ([]byte, error)) string {
	txt, err := derive()
	if err != nil {
		slog.Warn("dkim: signature exists but its key file can't be read; keeping both the signature and the published key",
			"domain", domainName, "path", path, "err", err)
		return ""
	}
	return string(txt)
}

// ensureDKIMRSAKey returns the PEM and TXT of the RSA key at path, generating
// and writing one when the file does not exist.
func ensureDKIMRSAKey(path string) ([]byte, string, error) {
	if _, err := os.Stat(path); err == nil {
		pemKey, err := dkim.LoadRSA(path)
		if err != nil {
			return nil, "", dkimInternal("load RSA DKIM key %s: %v", path, err)
		}
		txt, err := dkim.PublicRSADKIMTxtFromPEM(pemKey)
		if err != nil {
			return nil, "", dkimInternal("derive RSA DKIM TXT from %s: %v", path, err)
		}
		return pemKey, string(txt), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, "", dkimInternal("stat RSA DKIM key %s: %v", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, "", dkimInternal("mkdir DKIM dir: %v", err)
	}
	k, err := dkim.GenerateRSA()
	if err != nil {
		return nil, "", dkimInternal("generate RSA DKIM key: %v", err)
	}
	if err := dkim.WritePrivate(path, k.PrivatePEM); err != nil {
		return nil, "", dkimInternal("write RSA DKIM key: %v", err)
	}
	return k.PrivatePEM, string(k.PublicDKIMTxt), nil
}

func dkimInternal(format string, a ...any) error {
	return &agentwire.AgentError{Code: agentwire.CodeInternal, Message: fmt.Sprintf(format, a...)}
}

type dkimEnsureParams struct {
	DomainName string `json:"domain_name"`
}

type dkimEnsureResponse struct {
	Ok               bool     `json:"ok"`
	Ed25519Selector  string   `json:"ed25519_selector"`
	Ed25519PublicKey string   `json:"ed25519_public_key"`
	RSASelector      string   `json:"rsa_selector"`
	RSAPublicKey     string   `json:"rsa_public_key"`
	Created          []string `json:"created"`
}

// dkimEnsureHandler is domain_email.dkim_ensure, the reconciler's pass that
// gives every mail domain both signatures (GH #2475). A domain Stalwart does
// not know answers not_found: there is nothing to sign for yet.
func dkimEnsureHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p dkimEnsureParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if !domainRegex.MatchString(p.DomainName) {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("invalid domain_name %q", p.DomainName)}
	}
	domainID, err := domainIDByName(ctx, p.DomainName)
	if err != nil {
		return nil, err
	}
	if domainID == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeNotFound,
			Message: fmt.Sprintf("domain %q has no Stalwart registry entry", p.DomainName),
		}
	}
	res, err := ensureDomainDKIM(ctx, p.DomainName, domainID)
	if err != nil {
		return nil, err
	}
	created := res.Created
	if created == nil {
		created = []string{}
	}
	return dkimEnsureResponse{
		Ok:               true,
		Ed25519Selector:  dkimSelector,
		Ed25519PublicKey: res.Ed25519PublicKey,
		RSASelector:      dkimRSASelector,
		RSAPublicKey:     res.RSAPublicKey,
		Created:          created,
	}, nil
}

func init() {
	Default.Register("domain_email.dkim_ensure", dkimEnsureHandler)
}
