# Migration: WHM bulk migration → Agent v2

**Status**: design note only. Not yet implemented.

## Why this is not done alongside the other jobs

`RunWhmMigrationBatch` has 15 constructor parameters, one of which is
`$apiToken`. Tokens that access a remote WHM server MUST NOT land in:

- `background_tasks.payload` — that column is JSON, visible in the Filament
  dashboard, readable by any admin operator.
- The agent socket message — logged in audit trail in cleartext.
- The Laravel cache (by default, the Redis `cache:` prefix) — any process
  with Redis access can read it.

The secure pattern is:

1. Encrypt the WHM API token at-rest using `Crypt::encryptString()` before
   stashing it.
2. Store the encrypted blob in cache (or a dedicated `whm_migration_runs`
   table) keyed by a random nonce generated at dispatch time.
3. Pass ONLY the nonce via `--cache-key` to the spawned binary.
4. Decrypt inside the spawned binary.
5. Wipe the cache entry on task completion / failure.

The same pattern applies to any future migration type that takes credentials
(remote hosting providers, cloud backup endpoints, etc.).

## Proposed implementation sketch

```php
// In RunWhmMigrationBatch::handle()
if (config('jabali.agent_v2.whm_migration_enabled')) {
    $nonce = Str::random(32);
    $cacheKey = 'bg-task:whm-migration:'.$nonce;

    Cache::put($cacheKey, [
        'cache_key' => $this->cacheKey,
        'hostname' => $this->hostname,
        'whm_username' => $this->whmUsername,
        'api_token_enc' => Crypt::encryptString($this->apiToken), // ← encrypted at rest
        'port' => $this->port,
        'use_ssl' => $this->useSSL,
        'accounts' => $this->accounts,
        'selected_accounts' => $this->selectedAccounts,
        'restore_files' => $this->restoreFiles,
        'restore_databases' => $this->restoreDatabases,
        'restore_emails' => $this->restoreEmails,
        'restore_ssl' => $this->restoreSsl,
        'create_linux_users' => $this->createLinuxUsers,
    ], now()->addHours(3));

    $dispatcher->dispatch(
        type: BackgroundTaskType::WhmMigration,
        argv: [
            '/usr/bin/php', base_path('artisan'),
            'jabali:tasks:run-whm-migration',
            '--cache-key='.$cacheKey,
        ],
        payload: ['hostname' => $this->hostname, 'account_count' => count($this->selectedAccounts)],
        // NOTE: hostname goes in payload (visible), token does NOT.
        dedupeKey: 'whm-migration:'.md5($this->hostname.$this->cacheKey),
        limits: ['cpu' => 75, 'memory' => '4G', 'io' => 100],
    );
}
```

```php
// In RunWhmMigrationTaskCommand::handle()
$args = Cache::get($this->option('cache-key'));
$token = Crypt::decryptString($args['api_token_enc']);

$job = new RunWhmMigrationBatch(
    cacheKey: $args['cache_key'],
    hostname: $args['hostname'],
    whmUsername: $args['whm_username'],
    apiToken: $token,
    // ... etc
);
// Bypass the flag to avoid recursion, then run:
config(['jabali.agent_v2.whm_migration_enabled' => false]);
$job->handle($orchestrator, $statusStore);
```

## Checklist before implementing

- [ ] Decide whether cache-backed encrypted payload is acceptable, or
      whether a dedicated `whm_migration_runs` table is preferable (less
      dependent on cache eviction).
- [ ] Audit `Cache::put()` behavior under memory pressure — default Redis
      LRU eviction could drop the args before the binary reads them.
- [ ] Add retention policy: cache entry TTL should be long enough for
      the longest-running migration (currently no upper bound) plus a
      safety margin. `now()->addHours(3)` is probably too short for a
      real migration.
- [ ] Consider using a new short-lived DB table instead of cache:
      `create table migration_run_payloads (id uuid primary key,
      encrypted_data text not null, task_id uuid references
      background_tasks(id), created_at timestamp)`.
- [ ] On task completion, purge the cache entry (the run command should
      do this on both success and failure paths).
- [ ] Add integration test that verifies the token never appears in
      `background_tasks.payload`, audit log, or agent socket messages.

## Feature flag

Already reserved: `JABALI_AGENT_V2_WHM_MIGRATION`.

When implementing, also audit `RunCpanelRestore` to see if any of its
constructor args are comparably sensitive and would benefit from the
same encrypted-transport pattern.
