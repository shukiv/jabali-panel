package commands

import "strings"

// Alias ownership identity (GH #1145). The original #1053 model inferred which
// tenant owns a passwd alias from a SHARED uid (alias.uid == tenant.uid). That
// breaks for isolated (separate-uid) subaccounts, which no longer share the
// tenant uid — an isolated alias would be invisible to tenant suspension
// (JAB-254), the stray-alias reaper, and the per-tenant list, and the eligibility
// gate. It also relied on the uid check as the ONLY prefix-collision
// disambiguator (tenant "shop" vs a separate tenant "shop_other" both match the
// "shop_" prefix).
//
// The fix encodes the owning tenant IN the GECOS ownership marker so ownership
// is explicit in the host artifact — uid-model-independent AND collision-proof.
//
// Delimiter is "=", NOT ":": the marker lives in the GECOS field, which is
// itself a ":"-delimited field of /etc/passwd, so a colon would corrupt the
// line (and useradd --comment rejects it). "=" is valid in GECOS and can never
// appear in a Linux username (NAME_REGEX [a-z_][a-z0-9_-]*), so the split is
// unambiguous. The marker sits in the first GECOS comma-subfield, so gecosName
// (which strips at the first comma) still yields it even with trailing ",,,".

// ftpAliasGecosFor is the owner-encoded GECOS marker stamped on every alias the
// panel creates (legacy same-uid AND isolated separate-uid). Pre-existing
// accounts carry the bare ftpAliasGecos; ftpMarkerOwner recognizes both.
func ftpAliasGecosFor(tenantUsername string) string {
	return ftpAliasGecos + "=" + tenantUsername
}

// ftpMarkerOwner parses a passwd GECOS field. marked is true for any
// panel-owned alias (bare or owner-encoded). owner is the encoded tenant
// username, or "" for a bare legacy marker (ownership then inferred by the
// same-uid rule). Accepts the raw GECOS or a pre-split name segment.
func ftpMarkerOwner(gecos string) (owner string, marked bool) {
	seg := gecosName(gecos) // strip any trailing ",,," GECOS sub-fields
	if seg == ftpAliasGecos {
		return "", true
	}
	if rest, ok := strings.CutPrefix(seg, ftpAliasGecos+"="); ok && rest != "" {
		return rest, true
	}
	return "", false
}

// ftpAliasOwnedBy reports whether a passwd entry (its raw GECOS + numeric uid)
// is a panel-owned subaccount of tenant. Owner-encoded aliases match EXACTLY by
// owner — collision-proof across prefix-colliding tenants and independent of
// whether the alias shares the tenant uid. Bare-marker legacy aliases fall back
// to the shared-uid rule so existing accounts keep working with no reprovision.
func ftpAliasOwnedBy(gecos string, aliasUID int, tenant *ftpTenant) bool {
	owner, marked := ftpMarkerOwner(gecos)
	if !marked {
		return false
	}
	if owner != "" {
		// Owner match is authoritative, but never claim a SYSTEM uid: a
		// (root-created, mistaken) owner marker on uid 0 / a service account
		// must not be lockable/listable as a subaccount. An owned subaccount is
		// either the tenant uid (owner-encoded legacy mode) or an isolated uid
		// in the reserved range.
		return owner == tenant.Username && (aliasUID == tenant.UID || aliasUID >= ftpSubaccountUIDMin)
	}
	// Legacy bare marker: the alias must share the tenant uid (the historical
	// disambiguator that still distinguishes "shop" from "shop_other").
	return aliasUID == tenant.UID
}
