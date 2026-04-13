# Panel Integration Code Review

Review of `panel/` files before merging into Jabali Panel.

**Verdict: BLOCK** — 3 critical security issues must be fixed first.

---

## CRITICAL — Must fix before merge

### 1. Command injection via unvalidated parameters

**File:** `panel/agent/jabali-backup.php:214, 233`

`destination` and `exclude_accounts` are passed directly to CLI args without validation:

```php
// WRONG
$args[] = '--destination=' . $destination;
$args[] = '--exclude-accounts=' . $excludeAccounts;
```

An attacker could inject CLI flags like `--config=/etc/shadow` or `--help`.

**Fix:**
```php
// Validate destination
if ($destination !== '' && preg_match('/^[a-zA-Z0-9_\-\.]+$/', $destination)) {
    $args[] = '--destination=' . $destination;
}

// Validate exclude_accounts — each must pass username validation
if ($excludeAccounts !== '') {
    $safe = [];
    foreach (explode(',', $excludeAccounts) as $acct) {
        if (validateUsername(trim($acct))) {
            $safe[] = trim($acct);
        }
    }
    if (!empty($safe)) {
        $args[] = '--exclude-accounts=' . implode(',', $safe);
    }
}
```

### 2. Encryption key exposed in tooltip

**File:** `panel/filament/pages/Backups.php:222`

```php
// WRONG — raw key visible on hover, in dev tools, accessibility readers
->tooltip(fn (array $record): string => $record['encryption_key'] ?? '')
```

**Fix:** Remove the tooltip entirely:
```php
// No tooltip — key should never leave the server
// If admin needs it, provide a "Copy Key" action with confirmation
```

### 3. Path traversal in restore file selection

**File:** `panel/agent/jabali-backup.php:330`

```php
// WRONG — str_replace is insufficient
$file = str_replace(["\0", '..'], '', $file);
```

`str_replace('..', '')` can be bypassed with `....//` (after removal, `../` remains).

**Fix:**
```php
// Reject any file with traversal patterns
if (strpos($file, '..') !== false || strpos($file, "\0") !== false) {
    continue;
}
if (!preg_match('#^[a-zA-Z0-9/_\-\.\s]+$#', $file)) {
    continue;
}
$args[] = '--file=' . $file;
```

---

## HIGH — Should fix before merge

### 4. Inline styles — must use Tailwind only

**Files:** All Blade views in `panel/views/`

Jabali convention: **NEVER use inline `style=` attributes or custom CSS.** Always use Tailwind classes.

```blade
<!-- WRONG -->
<span style="font-family:monospace;font-weight:600">{{ $snap['id'] }}</span>
<div style="cursor:pointer">
<pre style="max-height:24rem;overflow:auto;background:#111827;color:#4ade80;padding:1rem">

<!-- CORRECT -->
<span class="font-mono font-semibold">{{ $snap['id'] }}</span>
<div class="cursor-pointer">
<pre class="max-h-96 overflow-auto bg-gray-950 text-green-400 p-4 rounded-lg text-xs">
```

Also, hardcoded dark colors (`#111827`, `#4ade80`) won't adapt to light mode. Use Tailwind's dark mode variants:

```blade
<pre class="max-h-96 overflow-auto bg-gray-100 dark:bg-gray-900 text-gray-800 dark:text-green-400 p-4 rounded-lg text-xs font-mono">
```

### 5. XSS risk in wire:click attributes

**File:** `panel/views/user/backups.blade.php:18, 35, 47, 54`

Paths interpolated with `{{ }}` can break out of the string context if they contain single quotes:

```blade
<!-- WRONG — breaks if path contains a single quote -->
<div wire:click="navigateTo('{{ $item['path'] }}')">

<!-- CORRECT — @json() handles escaping -->
<div wire:click="navigateTo(@json($item['path']))">
```

Fix all `wire:click` attributes that interpolate variables.

### 6. Filament v5 namespace check

Verify these imports exist in Filament v5. The panel uses v5 where namespaces changed significantly:

```php
// These are correct for v5:
use Filament\Schemas\Components\Grid;        // Layout
use Filament\Schemas\Components\Section;     // Layout
use Filament\Forms\Components\TextInput;     // Form field
use Filament\Actions\Action;                 // Actions

// This does NOT exist in v5:
use Filament\Forms\Components\Actions\Action;  // WRONG — use Filament\Actions\Action
```

### 7. Tab content not wrapped in skeleton

Tab content views should use the loading skeleton wrapper:

```blade
<!-- WRONG -->
<div class="space-y-6">
    {{ $this->table }}
</div>

<!-- CORRECT -->
<x-tab-loading-skeleton>
    <div class="space-y-6">
        {{ $this->table }}
    </div>
</x-tab-loading-skeleton>
```

This shows pulsing gray bars during tab switches instead of a flash of empty content. The global skeleton script is already loaded — you just need the wrapper.

---

## MEDIUM — Consider fixing

### 8. Missing username validation on panel side

**File:** `panel/filament/pages/Backups.php` — `browseSnapshot()`

The agent validates usernames, but the panel should too (defense in depth):

```php
public function browseSnapshot(string $snapshotId, string $username): void
{
    if (!preg_match('/^[a-z][a-z0-9_-]{0,31}$/', $username)) {
        Notification::make()->title(__('Invalid username'))->danger()->send();
        return;
    }
    // ...
}
```

### 9. Silent parse failures

**File:** `panel/agent/jabali-backup.php` — `jbListAccounts()`

If CLI output format changes, the regex silently fails and returns empty arrays. Add error detection:

```php
if (empty($accounts) && !empty($lines)) {
    return ['success' => false, 'error' => 'Failed to parse account list output'];
}
```

### 10. Unvalidated components array

**File:** `panel/agent/jabali-backup.php:297`

Validate against known component names before passing to `--only=`:

```php
$validComponents = ['files', 'mysql', 'postgres', 'email', 'dns', 'ssl', 'nginx', 'php', 'cron', 'wordpress', 'metadata'];
$safe = array_intersect($components, $validComponents);
```

### 11. Error messages not sanitized

User-facing error messages should use `SafeError::message($e)` to avoid leaking internal details:

```php
// WRONG
Notification::make()->body($e->getMessage())->danger()->send();

// CORRECT
use App\Support\SafeError;
Notification::make()->body(SafeError::message($e))->danger()->send();
```

### 12. Missing strict_types

Add to any file that doesn't have it:

```php
<?php

declare(strict_types=1);
```

---

## LOW — Style / Optional

### 13. Missing PHPDoc on agent handlers

Add `@param` and `@return` docs to all `jb*()` functions for maintainability.

### 14. Plural translations

```php
// WRONG
count($snapshots) . ' ' . __('snapshot(s)')

// CORRECT
trans_choice(':count snapshot|:count snapshots', count($snapshots), ['count' => count($snapshots)])
```

### 15. Use Formatter::bytes() for file sizes

```php
// WRONG
number_format($size / 1024 / 1024, 1) . ' MB'

// CORRECT
use App\Support\Formatter;
Formatter::bytes($size)
```

---

## Checklist before merge

- [ ] Fix all 3 CRITICAL security issues
- [ ] Replace all inline styles with Tailwind classes
- [ ] Fix wire:click XSS — use `@json()` for all interpolated values
- [ ] Verify Filament v5 imports compile
- [ ] Wrap tab content in `<x-tab-loading-skeleton>`
- [ ] Remove encryption key tooltip
- [ ] Add username validation on panel side
- [ ] Use `SafeError::message()` for user-facing errors
- [ ] Add `declare(strict_types=1)` to all files
- [ ] Test in both light and dark mode
