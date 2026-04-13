# ADR-0006: CMS-Aware False Positive Reduction

**Date**: 2026-04-10
**Status**: accepted
**Deciders**: Shuki Vaknin

## Context

WordPress installations were being quarantined en masse during install. A fresh WordPress install creates ~1500 files in under 60 seconds, triggering multiple behavioral and content-based findings that compound above the quarantine threshold (70). On the test server, 537 legitimate WordPress core and theme files were quarantined. The root causes: entropy threshold 4.5 flags normal PHP (4.5-5.5 range), burst creation threshold of 20 files/60s is far too low for CMS installs, the random filename regex matches pure alphabetic names like "functions", and CMS core path detection only covered wp-admin/wp-includes but not wp-content/themes/ or wp-content/plugins/.

## Decision

Four changes to the detection pipeline:
1. Extend `_is_cms_core_path()` to include `wp-content/themes/`, `wp-content/plugins/`, and `wp-content/mu-plugins/` (but explicitly NOT `wp-content/uploads/` which can contain malware). Files in these paths only count YARA/ClamAV signature matches.
2. Raise entropy threshold from 4.5 to 6.0. Normal PHP scores 4.5-5.5; obfuscated malware scores 6.5+.
3. Fix random filename regex to require at least one digit mixed with letters. Pure alphabetic names are not suspicious.
4. Raise burst file creation threshold from 20 to 100 files per 60 seconds.

## Alternatives Considered

### Alternative 1: Whitelist by WordPress checksum verification
- **Pros**: Exact match, zero false positives for verified core files
- **Cons**: Only works for WordPress core (not themes/plugins), requires API call to wordpress.org per install, doesn't solve entropy/behavior false positives
- **Why not**: Too narrow — doesn't address theme/plugin files or the underlying threshold problems

### Alternative 2: Disable behavioral scoring entirely for CMS directories
- **Pros**: Eliminates all CMS false positives
- **Cons**: Real attacks in CMS directories (e.g., backdoored plugin) would lose behavioral signal
- **Why not**: Too aggressive — we want behavioral signals for uploads/ and injected files

### Alternative 3: Add a cooldown period after detecting a CMS install
- **Pros**: Would suppress false positives during the install window
- **Cons**: Complex state machine, attacker could time uploads during cooldown
- **Why not**: Fragile and gameable; fixing the thresholds is simpler and more robust

## Consequences

### Positive
- WordPress/Joomla installs no longer trigger mass quarantine
- Theme and plugin files scored only on YARA/ClamAV signatures (high confidence)
- Entropy scanner focuses on genuinely obfuscated content (6.0+)
- Behavioral detection still works for uploads/ and non-CMS paths

### Negative
- Higher entropy threshold (6.0) may miss some moderately obfuscated payloads in the 5.0-6.0 range — mitigated by YARA rules which catch these patterns with multi-condition matching
- Higher burst threshold (100) means a real attack creating 50-99 files in 60s won't trigger burst detection — mitigated by individual file scoring from YARA/heuristic patterns

### Risks
- Attackers could place malware in wp-content/themes/ knowing behavioral/entropy findings are filtered — mitigated by YARA rules which still scan all files regardless of path
