// domain.email_dkim_rotate — replace a mail domain's DKIM keys (ADR-0043
// §"Rotation"), on the operator's request: `jabali domain
// email-dkim-rotate <domain>`, or Rotate DKIM on the domain's mail page.
//
// Stalwart signs with the private keys in its registry (x:DkimSignature), not
// with the files under dkimKeyDir: those are the panel's copy, from which the
// published TXT is derived. Changing only the file published a key nothing
// signed with, so each key is rotated Stalwart first:
//  1. generate it and stage it as <path>.new;
//  2. set it on the domain's signatures of that key, or create the DKIM1
//     signature when the domain has none;
//  3. once Stalwart has it, copy the current file to <path>.old and rename
//     <path>.new over it.
//
// Stalwart refusing a key leaves that key as it was. An update applies to
// the next message without a reload (it drops the domain's cached signers).
//
// The Ed25519 key goes first; its failure fails the rotation with nothing
// changed. Then the DKIM2 signature, which shares the Ed25519 key (GH #648),
// and the RSA key (GH #2475): their failure is a warning, and an RSA key that
// was not rotated is left out of the response so the panel keeps publishing
// the key Stalwart still signs with.
//
// The selectors stay the same. A receiver holding the old TXT in its DNS
// cache fails the new signatures until the record expires, and a message
// signed before the rotation and still queued for retry fails at its
// receiver. <path>.old keeps the previous private key as a record; restoring
// it is not a rollback, since Stalwart would keep signing with the new key.
package commands

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/dkim"
)

type domainEmailDKIMRotateParams struct {
	DomainName string `json:"domain_name"`
}

type domainEmailDKIMRotateResponse struct {
	OldDKIMPublicKey string `json:"old_dkim_public_key,omitempty"`
	NewDKIMPublicKey string `json:"new_dkim_public_key"`
	OldKeyBackupPath string `json:"old_key_backup_path,omitempty"`
	// The RSA key's TXT values (GH #2475). New is absent when the RSA key
	// was not rotated, Old when the domain had none.
	OldDKIMRSAPublicKey string `json:"old_dkim_rsa_public_key,omitempty"`
	NewDKIMRSAPublicKey string `json:"new_dkim_rsa_public_key,omitempty"`
	// Warnings name the keys or signatures that were not rotated.
	Warnings []string `json:"warnings,omitempty"`
}

func init() {
	Default.Register("domain.email_dkim_rotate", domainEmailDKIMRotateHandler)
}

func domainEmailDKIMRotateHandler(ctx context.Context, raw json.RawMessage) (any, error) {
	var p domainEmailDKIMRotateParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "malformed JSON: " + err.Error(),
		}
	}
	if p.DomainName == "" {
		return nil, &agentwire.AgentError{
			Code: agentwire.CodeInvalidArgument, Message: "domain_name required",
		}
	}
	if err := validateDomainNameForShell(p.DomainName); err != nil {
		return nil, err
	}

	edPath := filepath.Join(dkimKeyDirFunc(), p.DomainName+".key")
	oldSeed, err := dkim.LoadEd25519(edPath)
	if err != nil {
		// Rotation applies to a domain whose mail is set up; email_enable
		// creates the key.
		if errors.Is(err, os.ErrNotExist) {
			return nil, &agentwire.AgentError{
				Code: agentwire.CodeFailedPrecondition,
				Message: fmt.Sprintf(
					"no existing DKIM key at %s — run domain.email_enable first; rotation only applies to already-enabled domains",
					edPath),
			}
		}
		return nil, dkimInternal("load existing DKIM key %s: %v", edPath, err)
	}
	oldTXT, err := dkim.PublicDKIMTxtFromSeed(oldSeed)
	if err != nil {
		return nil, dkimInternal("derive old DKIM TXT: %v", err)
	}
	domainID, err := domainIDByName(ctx, p.DomainName)
	if err != nil {
		return nil, err
	}
	if domainID == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeFailedPrecondition,
			Message: fmt.Sprintf("domain %q has no Stalwart registry entry — is email enabled?", p.DomainName),
		}
	}
	resp := domainEmailDKIMRotateResponse{OldDKIMPublicKey: string(oldTXT)}

	kp, err := dkim.GenerateEd25519()
	if err != nil {
		return nil, dkimInternal("generate fresh DKIM key: %v", err)
	}
	seed, err := base64.StdEncoding.DecodeString(string(kp.PrivateRawBase64))
	if err != nil {
		return nil, dkimInternal("decode fresh DKIM key: %v", err)
	}
	edPEM, err := dkim.SeedToPKCS8PEM(seed)
	if err != nil {
		return nil, dkimInternal("wrap DKIM seed as PKCS#8 PEM: %v", err)
	}
	backup, err := rotateDKIMKey(ctx, domainID, dkim1Ed25519Type, dkimSelector, edPath, kp.PrivateRawBase64, string(edPEM))
	if err != nil {
		return nil, err
	}
	resp.NewDKIMPublicKey = string(kp.PublicDKIMTxt)
	resp.OldKeyBackupPath = backup

	dkim2IDs, err := dkim2SignatureIDs(ctx, domainID)
	if err == nil && len(dkim2IDs) > 0 {
		err = setDkimSignatureKey(ctx, dkim2IDs, string(edPEM))
	}
	if err != nil {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf(
			"The DKIM2 signature may still use the previous Ed25519 key, so its signatures would fail: turn DKIM2 signing off and on again in Server Settings (%v).", err))
	}

	rsaPath := dkimRSAKeyPath(p.DomainName)
	oldRSA := ""
	if pemKey, err := dkim.LoadRSA(rsaPath); err == nil {
		if txt, err := dkim.PublicRSADKIMTxtFromPEM(pemKey); err == nil {
			oldRSA = string(txt)
		}
	}
	k, err := dkim.GenerateRSA()
	if err == nil {
		_, err = rotateDKIMKey(ctx, domainID, dkim1RSAType, dkimRSASelector, rsaPath, k.PrivatePEM, string(k.PrivatePEM))
	}
	if err != nil {
		resp.Warnings = append(resp.Warnings, fmt.Sprintf(
			"The RSA DKIM key was not rotated and keeps signing with its current key (%v).", err))
	} else {
		resp.NewDKIMRSAPublicKey = string(k.PublicDKIMTxt)
		resp.OldDKIMRSAPublicKey = oldRSA
	}
	return resp, nil
}

// rotateDKIMKey puts a new key on domainID's signatures of type typ, then in
// the file at path, in that order (see the file comment). fileKey is the
// key's on-disk form, stalwartKey the PEM Stalwart takes. It returns the path
// of the previous key's copy, or "" when there was none.
func rotateDKIMKey(ctx context.Context, domainID, typ, selector, path string, fileKey []byte, stalwartKey string) (string, error) {
	staged := path + ".new"
	if err := dkim.WritePrivate(staged, fileKey); err != nil {
		return "", dkimInternal("stage the new DKIM key at %s: %v", staged, err)
	}
	ids, err := dkimSignatureIDs(ctx, domainID, typ)
	if err == nil {
		if len(ids) == 0 {
			err = createDkim1Signature(ctx, typ, domainID, selector, stalwartKey)
		} else {
			err = setDkimSignatureKey(ctx, ids, stalwartKey)
		}
	}
	if err != nil {
		_ = os.Remove(staged)
		return "", err
	}

	backup := ""
	if _, err := os.Stat(path); err == nil {
		if err := copyFileMode(path, path+".old", 0o600); err != nil {
			// The previous key is retired either way; only its copy is lost.
			slog.Warn("dkim rotate: could not keep a copy of the previous key", "path", path, "err", err)
		} else {
			backup = path + ".old"
		}
	}
	if err := os.Rename(staged, path); err != nil {
		return "", dkimInternal("Stalwart signs with the new DKIM key, but %s could not replace %s: %v", staged, path, err)
	}
	return backup, nil
}

// setDkimSignatureKey replaces the private key of the signatures ids. JMAP
// applies each update on its own, so with several ids one can be refused
// after another took the new key; the panel creates one signature per type,
// so a domain it manages has a single id here.
func setDkimSignatureKey(ctx context.Context, ids []string, pemPrivateKey string) error {
	update := make(map[string]any, len(ids))
	for _, id := range ids {
		update[id] = map[string]any{
			"privateKey": map[string]any{"@type": "Text", "secret": pemPrivateKey},
		}
	}
	var result jmapSetResult
	if err := jmapCall(ctx, "x:DkimSignature/set", map[string]any{"update": update}, &result); err != nil {
		return err
	}
	for _, id := range ids {
		if _, ok := result.Updated[id]; ok {
			continue
		}
		if reason, ok := result.NotUpdated[id]; ok {
			return dkimInternal("stalwart refused the new key for DKIM signature %s: %s", id, string(reason))
		}
		return dkimInternal("stalwart x:DkimSignature/set: no result for DKIM signature %s", id)
	}
	return nil
}

// copyFileMode does a straight copy from src to dst with the given
// dst file mode. Uses io.Copy; chmod ensures perms even on a fresh
// create. Used by the rotation handler to keep the previous key.
func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Chmod(dst, mode); err != nil {
		return err
	}
	return nil
}
