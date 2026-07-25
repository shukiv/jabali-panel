// Package notifsecret seals/opens the secret fields of a notification channel
// config at rest (JAB-171 phase 3). Bot tokens, bearer tokens, HMAC secrets and
// SMTP passwords must never sit in the DB as plaintext nor be echoed by any GET.
//
// A sealed value is stored in-place in the same string field as
// `enc:1:<base64url(ssokey envelope)>`. This lets a single JSON blob hold a mix
// of plaintext public fields (url, topic, chat id) and sealed secret fields,
// and lets a pre-JAB-171 plaintext row be detected (no prefix) and backfilled.
//
// This package is a leaf: it imports only models + ssokey, so both the
// repository (seal on write) and the notifications dispatcher (open on send)
// can use it without an import cycle.
package notifsecret

import (
	"encoding/base64"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

const sealPrefix = "enc:1:"

// secretPtrs returns pointers to every secret string field on the config so
// Seal/Open/Redact can treat them uniformly. Add new secret fields here.
func secretPtrs(cfg *models.NotificationChannelConfig) []*string {
	return []*string{
		&cfg.Bearer,
		&cfg.HMACSecret,
		&cfg.SMTPPassword,
		&cfg.BotToken,
	}
}

func isSealed(s string) bool { return strings.HasPrefix(s, sealPrefix) }

// Seal encrypts every non-empty, not-already-sealed secret field in place.
// Idempotent: an already-sealed field is left untouched, so re-sealing a row
// (or sealing a row with a mix of new plaintext + existing sealed fields) is
// safe.
func Seal(cfg *models.NotificationChannelConfig, key ssokey.Key) error {
	for _, p := range secretPtrs(cfg) {
		if *p == "" || isSealed(*p) {
			continue
		}
		env, err := key.Seal([]byte(*p))
		if err != nil {
			return err
		}
		*p = sealPrefix + base64.RawURLEncoding.EncodeToString(env)
	}
	return nil
}

// Open decrypts every sealed secret field in place. A field without the seal
// prefix is treated as legacy plaintext and left as-is (defensive during the
// backfill window). Call this only on an in-memory copy about to be handed to a
// sender — never persist an opened config.
func Open(cfg *models.NotificationChannelConfig, key ssokey.Key) error {
	for _, p := range secretPtrs(cfg) {
		if !isSealed(*p) {
			continue
		}
		env, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(*p, sealPrefix))
		if err != nil {
			return err
		}
		pt, err := key.Open(env)
		if err != nil {
			return err
		}
		*p = string(pt)
	}
	return nil
}

// Redact blanks every secret field and reports whether any was set. Use before
// returning a channel from any API GET so a secret is never echoed. Operates on
// the passed config, so pass a copy.
func Redact(cfg *models.NotificationChannelConfig) (hasSecret bool) {
	for _, p := range secretPtrs(cfg) {
		if *p != "" {
			hasSecret = true
			*p = ""
		}
	}
	return hasSecret
}

// PreserveEmptySecrets implements write-only secret semantics: for each secret
// field the incoming config leaves empty, restore the previous value. This lets
// an edit that doesn't re-enter a token (because a GET redacted it) keep the
// stored secret instead of wiping it. Call before Seal on the update path.
func PreserveEmptySecrets(next *models.NotificationChannelConfig, prev models.NotificationChannelConfig) {
	np := secretPtrs(next)
	pp := secretPtrs(&prev)
	for i := range np {
		if *np[i] == "" {
			*np[i] = *pp[i]
		}
	}
}

// HasSealed reports whether any secret field is currently sealed (used by the
// backfill to skip already-migrated rows without a key).
func HasSealed(cfg *models.NotificationChannelConfig) bool {
	for _, p := range secretPtrs(cfg) {
		if isSealed(*p) {
			return true
		}
	}
	return false
}

// HasPlaintextSecret reports whether any secret field holds an unsealed
// non-empty value — i.e. a row that still needs backfilling.
func HasPlaintextSecret(cfg *models.NotificationChannelConfig) bool {
	for _, p := range secretPtrs(cfg) {
		if *p != "" && !isSealed(*p) {
			return true
		}
	}
	return false
}
