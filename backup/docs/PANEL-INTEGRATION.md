# Jabali Panel Integration Guide

Instructions for building the Backups UI in the Jabali Panel using Filament v5.

## Architecture Overview

```
Browser → Filament Page (PHP) → AgentClient → Unix Socket → jabali-agent (root)
                                                                ↓
                                                        jabali-backup CLI
```

The panel runs as `www-data`. It cannot run `jabali-backup` directly (needs root). All privileged operations go through the **agent** — a PHP daemon running as root on a Unix socket at `/var/run/jabali/agent.sock`.

### Key files installed by the addon

| File | Purpose |
|------|---------|
| `/etc/jabali/agent.d/jabali-backup.php` | Agent RPC routes (addon, not patching core agent) |
| `app/Filament/Admin/Pages/Backups.php` | Admin backup page |
| `app/Filament/Admin/Pages/SnapshotBrowser.php` | Admin snapshot file browser |
| `app/Filament/Jabali/Pages/UserBackups.php` | User backup page |
| `app/Backup/BackupServiceProvider.php` | Registers addon with Laravel |
| `app/Backup/Adapters/ResticSnapshotAdapter.php` | Restic snapshot data adapter |
| `public/backup-download.php` | Streaming download endpoint |
| `app/Services/Agent/AgentClient.php` | How the panel talks to the agent (core Jabali, not modified) |

## Step 1: Agent RPC Routes

The agent supports **addon route files** — no need to patch the monolith. Drop a PHP file in `/etc/jabali/agent.d/` that returns an array of action → handler mappings.

### Create the addon route file

Create `agent/jabali-backup.php` in your repo. The installer copies it to `/etc/jabali/agent.d/jabali-backup.php`:

```php
<?php
// /etc/jabali/agent.d/jabali-backup.php
// Agent addon routes for jabali-backup

// Load handler functions
require_once '/usr/local/lib/jabali-backup/agent-handlers.php';

return [
    'backup.list_snapshots' => fn(array $p) => backupListSnapshots($p),
    'backup.run' => fn(array $p) => backupRun($p),
    'backup.restore' => fn(array $p) => backupRestore($p),
    'backup.forget' => fn(array $p) => backupForget($p),
    'backup.browse' => fn(array $p) => backupBrowse($p),
    'backup.download_pipe' => fn(array $p) => backupDownloadPipe($p),
];
```

The agent loads all `*.php` files from `/etc/jabali/agent.d/` automatically. Addon routes are checked after core routes — if an action isn't in the core `match` statement, it falls through to addons.

### Example agent function

```php
function backupListSnapshots(array $params): array
{
    $username = $params['username'] ?? '';

    // Build command
    $cmd = ['/usr/local/bin/jabali-backup', 'list', 'snapshots'];
    if (!empty($username) && validateUsername($username)) {
        $cmd[] = '--account=' . $username;
    }
    $cmd[] = '--json';

    $proc = proc_open($cmd, [1 => ['pipe', 'w'], 2 => ['pipe', 'w']], $pipes);
    $stdout = stream_get_contents($pipes[1]);
    $stderr = stream_get_contents($pipes[2]);
    fclose($pipes[1]);
    fclose($pipes[2]);
    $exitCode = proc_close($proc);

    $data = json_decode($stdout, true);

    if ($exitCode !== 0 || $data === null) {
        return ['success' => false, 'error' => trim($stderr ?: $stdout ?: 'Command failed')];
    }

    return ['success' => true, 'snapshots' => $data];
}

function backupRun(array $params): array
{
    $username = $params['username'] ?? '';

    if (empty($username) || !validateUsername($username)) {
        return ['success' => false, 'error' => 'Invalid username'];
    }

    // Run in background — backup can take minutes
    $logFile = '/tmp/jabali-backup-' . $username . '-' . date('YmdHis') . '.log';
    $cmd = sprintf(
        'nohup /usr/local/bin/jabali-backup run --account=%s > %s 2>&1 &',
        escapeshellarg($username),
        escapeshellarg($logFile)
    );

    proc_open(['bash', '-c', $cmd], [], $pipes);

    return ['success' => true, 'message' => "Backup started for $username", 'log' => $logFile];
}

function backupRestore(array $params): array
{
    $username = $params['username'] ?? '';
    $snapshotId = $params['snapshot_id'] ?? 'latest';
    $components = $params['components'] ?? []; // e.g. ['files', 'mysql', 'email']
    $force = $params['force'] ?? false;

    if (empty($username) || !validateUsername($username)) {
        return ['success' => false, 'error' => 'Invalid username'];
    }

    if (!preg_match('/^[a-f0-9]+$|^latest$/', $snapshotId)) {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }

    $cmd = ['/usr/local/bin/jabali-backup', 'restore', $username,
            '--snapshot=' . $snapshotId];

    if (!empty($components)) {
        $cmd[] = '--only=' . implode(',', $components);
    }
    if ($force) {
        $cmd[] = '--force';
    }

    $proc = proc_open($cmd, [1 => ['pipe', 'w'], 2 => ['pipe', 'w']], $pipes);
    $stdout = stream_get_contents($pipes[1]);
    $stderr = stream_get_contents($pipes[2]);
    fclose($pipes[1]);
    fclose($pipes[2]);
    $exitCode = proc_close($proc);

    if ($exitCode !== 0) {
        return ['success' => false, 'error' => trim($stderr ?: $stdout)];
    }

    return ['success' => true, 'output' => trim($stdout)];
}
```

### Important agent rules

- **Always use `proc_open()` with array syntax** for commands — prevents shell injection. Use `escapeshellarg()` only when you must use shell strings (e.g. background processes).
- **Always validate usernames** with `validateUsername()` (alphanumeric + underscore + dash)
- **Validate snapshot IDs** with regex (`/^[a-f0-9]+$/`)
- **Long operations must run in background** — the agent has a socket timeout
- **Never pass passwords as CLI arguments** — use files or environment variables
- **Return JSON arrays** with `success` key (bool) and either `error` or data keys

## Step 2: Panel Side — Calling the Agent

The panel uses `AgentClient` to talk to the agent:

```php
use App\Services\Agent\AgentClient;

// In any Filament page or action:
$agent = app(AgentClient::class);

// Simple call
$result = $agent->send('backup.list_snapshots', ['username' => 'shuki']);

if (!($result['success'] ?? false)) {
    // Handle error
    Notification::make()->title('Failed')->body($result['error'])->danger()->send();
    return;
}

$snapshots = $result['snapshots'];
```

### AgentClient timeout

Default timeout is 120 seconds. For long operations, either:
- Run the backup in background on the agent side (recommended)
- Or increase timeout: `new AgentClient('/var/run/jabali/agent.sock', 300)`

## Step 3: Admin Backups Page

Create `app/Filament/Admin/Pages/Backups.php`. The admin page should:

1. **List all snapshots** across all users (table with user filter)
2. **Create backup** for a specific user or all users
3. **Restore** from a snapshot
4. **Delete/forget** old snapshots
5. **Download** a snapshot

### Filament page structure

```php
<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Services\Agent\AgentClient;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
// ... other imports

class Backups extends Page
{
    protected static string $navigationIcon = 'heroicon-o-cloud-arrow-up';
    protected static ?int $navigationSort = 11;
    protected string $view = 'filament.admin.pages.backups';

    public array $snapshots = [];

    public function mount(): void
    {
        $this->loadSnapshots();
    }

    public function loadSnapshots(): void
    {
        try {
            $result = app(AgentClient::class)->send('backup.list_snapshots', []);
            $this->snapshots = $result['snapshots'] ?? [];
        } catch (\Throwable $e) {
            $this->snapshots = [];
        }
    }
}
```

### Filament conventions in this project

- **Translations**: Always wrap labels in `__('...')` — the panel supports 8 languages
- **Notifications**: Use `Filament\Notifications\Notification` for success/error feedback
- **Actions**: Use `Filament\Actions\Action` for buttons with optional modal forms
- **Tables**: Use `Filament\Tables` for data display (supports sorting, filtering, searching)
- **Sections**: Use `Filament\Schemas\Components\Section` for grouping form fields
- **Grid**: Use `Filament\Schemas\Components\Grid` for multi-column layouts
- **Icons**: Use Heroicons (`heroicon-o-*` for outlined, `heroicon-s-*` for solid)
- **Error display**: Use `App\Support\SafeError::message($e)` to sanitize error messages for users

### Example: Create Backup action

```php
protected function getHeaderActions(): array
{
    return [
        Action::make('createBackup')
            ->label(__('Create Backup'))
            ->icon('heroicon-o-cloud-arrow-up')
            ->color('primary')
            ->form([
                Select::make('username')
                    ->label(__('Account'))
                    ->options(fn () => User::where('is_active', true)->pluck('username', 'username'))
                    ->required()
                    ->searchable(),
                Grid::make(3)->schema([
                    Toggle::make('include_files')->label(__('Files'))->default(true),
                    Toggle::make('include_databases')->label(__('Databases'))->default(true),
                    Toggle::make('include_email')->label(__('Email'))->default(true),
                ]),
            ])
            ->action(function (array $data): void {
                try {
                    $result = app(AgentClient::class)->send('backup.run', [
                        'username' => $data['username'],
                    ]);

                    if ($result['success'] ?? false) {
                        Notification::make()
                            ->title(__('Backup started'))
                            ->body(__('Running in background for :user', ['user' => $data['username']]))
                            ->success()
                            ->send();
                    } else {
                        throw new \Exception($result['error'] ?? 'Unknown error');
                    }
                } catch (\Exception $e) {
                    Notification::make()
                        ->title(__('Backup failed'))
                        ->body(SafeError::message($e))
                        ->danger()
                        ->send();
                }
            }),
    ];
}
```

### Snapshots table

Filament Tables normally use Eloquent queries. Since snapshots come from restic (not the database), you have two options:

1. **Cache snapshots in a DB table** — sync periodically via scheduled command. Lets you use Filament Tables natively with sorting/filtering.
2. **Use a Blade view with manual table** — call the agent, render HTML yourself. Simpler but no built-in sorting/filtering.

Option 1 is recommended. Create a `backup_snapshots` table with columns: `id`, `snapshot_id`, `username`, `size_bytes`, `created_at`. Sync via `jabali-backup list snapshots --json` on a schedule.

## Step 4: User Backups Page

Create `app/Filament/Jabali/Pages/Backups.php`. Similar to admin but scoped to the logged-in user:

```php
// User page always filters by current user
$result = app(AgentClient::class)->send('backup.list_snapshots', [
    'username' => auth()->user()->username,
]);
```

**Important**: The user page runs under the `web` guard (`auth()->user()`), not `admin`. Always scope queries to the authenticated user. Never trust user-supplied usernames — always use `auth()->user()->username`.

## Step 5: Download (Streaming via Named Pipe)

For downloading snapshots without creating temp files on the server:

### Agent side

```php
function backupDownloadPipe(array $params): array
{
    $username = $params['username'] ?? '';
    $snapshotId = $params['snapshot_id'] ?? 'latest';

    if (empty($username) || !validateUsername($username)) {
        return ['success' => false, 'error' => 'Invalid username'];
    }
    if (!preg_match('/^[a-f0-9]+$|^latest$/', $snapshotId)) {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }

    $pipeDir = '/tmp/jabali-exports';
    if (!is_dir($pipeDir)) mkdir($pipeDir, 0700, true);

    $pipePath = $pipeDir . '/pipe-' . bin2hex(random_bytes(8));
    posix_mkfifo($pipePath, 0600);
    chmod($pipePath, 0644); // www-data needs to read it

    // Fully detach with nohup — otherwise proc_open blocks
    $cmd = sprintf(
        "nohup bash -c '/usr/local/bin/jabali-backup dump %s --snapshot=%s > %s; rm -f %s' > /dev/null 2>&1 &",
        escapeshellarg($username),
        escapeshellarg($snapshotId),
        escapeshellarg($pipePath),
        escapeshellarg($pipePath)
    );
    proc_open(['bash', '-c', $cmd], [], $pipes);

    return ['success' => true, 'pipe' => $pipePath];
}
```

### Controller side

```php
// In app/Http/Controllers/BackupDownloadController.php:

use Symfony\Component\HttpFoundation\StreamedResponse;

public function adminDownload(Request $request): StreamedResponse|\Illuminate\Http\Response
{
    $user = Auth::guard('admin')->user();
    if (!$user || !$user->is_admin) {
        abort(403);
    }

    $result = app(AgentClient::class)->send('backup.download_pipe', [
        'username' => $request->query('username'),
        'snapshot_id' => $request->query('snapshot', 'latest'),
    ]);

    if (!($result['success'] ?? false)) {
        return response($result['error'] ?? 'Export failed', 500)
            ->header('Content-Type', 'text/plain');
    }

    $pipePath = $result['pipe'];
    $filename = $request->query('username') . '-backup.tar.gz';

    return response()->stream(function () use ($pipePath): void {
        $fp = fopen($pipePath, 'rb');
        if (!$fp) return;
        while (!feof($fp)) {
            $chunk = fread($fp, 65536);
            if ($chunk !== false && $chunk !== '') {
                echo $chunk;
                flush();
            }
        }
        fclose($fp);
        @unlink($pipePath);
    }, 200, [
        'Content-Type' => 'application/gzip',
        'Content-Disposition' => 'attachment; filename="' . $filename . '"',
        'Cache-Control' => 'no-cache',
    ]);
}
```

### Important: fully detach background processes

When starting background processes in the agent, you MUST use `nohup` and redirect all output to `/dev/null`. Otherwise the process blocks waiting for the parent:

```php
// WRONG — blocks:
proc_open(['bash', '-c', 'restic dump ... > $pipe &'], [], $pipes);

// CORRECT — fully detached:
$cmd = "nohup bash -c 'restic dump ... > $pipe' > /dev/null 2>&1 &";
proc_open(['bash', '-c', $cmd], [], $pipes);
```

## Step 6: Blade Views

Filament pages need a Blade view. Place them in:
- `resources/views/filament/admin/pages/backups.blade.php`
- `resources/views/filament/jabali/pages/backups.blade.php`

Minimal view for a page with a table:

```blade
<x-filament-panels::page>
    {{ $this->table }}
</x-filament-panels::page>
```

For tabbed pages (backups + schedules + logs):

```blade
<x-filament-panels::page>
    <x-filament::tabs>
        <x-filament::tabs.item
            :active="$activeTab === 'backups'"
            wire:click="$set('activeTab', 'backups')"
        >
            {{ __('Backups') }}
        </x-filament::tabs.item>
        <x-filament::tabs.item
            :active="$activeTab === 'schedules'"
            wire:click="$set('activeTab', 'schedules')"
        >
            {{ __('Schedules') }}
        </x-filament::tabs.item>
    </x-filament::tabs>

    {{ $this->table }}
</x-filament-panels::page>
```

## Step 7: Routes

Register download routes in `routes/web.php`:

```php
use App\Http\Controllers\BackupDownloadController;

// User panel
Route::get('/jabali-panel/backup-download', [BackupDownloadController::class, 'userDownload'])
    ->middleware(['web', 'auth'])
    ->name('filament.jabali.pages.backup-download');

// Admin panel
Route::get('/jabali-admin/backup-download', [BackupDownloadController::class, 'adminDownload'])
    ->middleware(['web', 'auth:admin'])
    ->name('filament.admin.pages.backup-download');
```

## Step 8: Scheduling Integration

The panel has a Laravel scheduler in `routes/console.php`. To sync snapshot data into the DB:

```php
Schedule::command('backups:sync-snapshots')
    ->everyFifteenMinutes()
    ->withoutOverlapping();
```

This artisan command should call `jabali-backup list snapshots --json` via the agent and sync results into a `backup_snapshots` table.

## Things to Know

### Filament v5 namespaces

| Component type | Namespace |
|---|---|
| Layout (Section, Grid, Tabs, Wizard) | `Filament\Schemas\Components\` |
| Form fields (TextInput, Select, Toggle) | `Filament\Forms\Components\` |
| Actions | `Filament\Actions\` |
| Table columns | `Filament\Tables\Columns\` |
| Get/Set utilities | `Filament\Schemas\Components\Utilities\` |

`FormAction` closures in `->action()` can silently fail in some contexts — if an action doesn't fire, try `wire:click` in Blade instead.

### Security rules

- **Never hardcode credentials** in source files
- **Use `proc_open()` with array syntax** for commands in the agent — prevents shell injection
- **Use `escapeshellarg()`** only when shell string is unavoidable (background processes)
- **Validate ownership** — user panel must only access their own backups
- **Validate paths** with `PathSanitizer::clean()` for any user-supplied file paths
- **Sanitize errors** with `SafeError::message($e)` before showing to users
- The agent socket must never be exposed over the network

### Code style

- Run `vendor/bin/pint --dirty` before committing (Laravel Pint formatter)
- Run `php artisan test --compact` after changes
- Use `declare(strict_types=1);` in all PHP files
- Wrap all user-facing strings in `__('...')` for translation
- Use `App\Support\Formatter::bytes($size)` for human-readable file sizes

### Testing

- PHPUnit only (no Pest)
- Tests go in `tests/Feature/`
- Use `php artisan make:test BackupPageTest` to create
- Run with `php artisan test --compact --filter=Backup`
- Mock `AgentClient` to test page logic without a real agent:

```php
$agent = Mockery::mock(AgentClient::class);
$agent->shouldReceive('send')
    ->with('backup.list_snapshots', Mockery::type('array'))
    ->andReturn(['success' => true, 'snapshots' => [...]]);
$this->app->instance(AgentClient::class, $agent);
```

### Git workflow

- Push to Gitea first for testing: `git push gitea main`
- Push to GitHub when stable: `GIT_SSH_COMMAND="ssh -i ~/.ssh/id_ed25519_github" git push origin main`
- Deploy: `jabali update` on the server (pulls, migrates, rebuilds caches)

### Cross-repo context

Read `~/projects/jabali-shared/CONTEXT.md` before making changes that touch the agent API. Update the change log there when adding new RPC routes.

## Embedding the File Browser for Snapshot Browsing

The panel has a built-in file browser (`app/FileBrowser/`) with an adapter pattern. You can reuse it to let users browse backup snapshot contents without building your own file tree UI.

### How the file browser works

```
FileBrowser Page → FileBrowserAdapter interface → FileOperations interface
                                                      ↓
                                              AgentAdapter (live files)
                                              LocalAdapter (local disk)
                                              SftpAdapter (remote SFTP)
                                              YOUR: ResticSnapshotAdapter ← add this
```

The file browser is a Filament plugin (`FileBrowserPlugin`) registered in `app/Providers/Filament/JabaliPanelProvider.php`. The adapter is bound in `AppServiceProvider::boot()`.

### The interfaces

```php
// app/FileBrowser/Adapters/FileBrowserAdapter.php
interface FileBrowserAdapter
{
    public function files(): FileOperations;
    public function archiver(): ?Archiver;      // null = disable extract
    public function permissions(): ?PermissionManager; // null = disable chmod
}

// app/FileBrowser/Adapters/FileOperations.php
interface FileOperations
{
    public function list(string $path, bool $showHidden = false): array;
    // Returns: ['items' => [['name', 'path', 'is_dir', 'size', 'modified', 'permissions'], ...]]

    public function read(string $path): array;
    // Returns: ['content' => '...']

    public function write(string $path, string $content): array;
    public function delete(string $path): array;
    public function mkdir(string $path): array;
    public function rename(string $oldPath, string $newPath): array;
    public function copy(string $source, string $destination): array;
    public function move(string $source, string $destination): array;
    public function upload(string $directory, string $filename, string $content): array;
    public function info(string $path): array;
    // Returns: ['info' => ['permissions' => 'drwxr-xr-x']]
}
```

### Create ResticSnapshotAdapter

For backup browsing, you only need **read-only** operations (`list`, `read`, `info`). Write operations should throw:

```php
<?php

declare(strict_types=1);

namespace App\Backup\Adapters;

use App\FileBrowser\Adapters\Archiver;
use App\FileBrowser\Adapters\FileBrowserAdapter;
use App\FileBrowser\Adapters\FileOperations;
use App\FileBrowser\Adapters\PermissionManager;
use App\Services\Agent\AgentClient;
use RuntimeException;

class ResticSnapshotAdapter implements FileBrowserAdapter, FileOperations
{
    public function __construct(
        private AgentClient $agent,
        private string $snapshotId,
        private string $username,
    ) {}

    public function files(): FileOperations
    {
        return $this;
    }

    public function archiver(): ?Archiver
    {
        return null; // No archive extraction in snapshots
    }

    public function permissions(): ?PermissionManager
    {
        return null; // Read-only, no permission changes
    }

    public function list(string $path, bool $showHidden = false): array
    {
        // Call agent which shells out to: jabali-backup ls <username> --snapshot=<id> --path=<path>
        $result = $this->agent->send('backup.browse', [
            'username' => $this->username,
            'snapshot_id' => $this->snapshotId,
            'path' => $path,
        ]);

        if (!($result['success'] ?? false)) {
            throw new RuntimeException($result['error'] ?? 'Failed to browse snapshot');
        }

        return ['items' => $result['items'] ?? []];
    }

    public function read(string $path): array
    {
        // Uses restic dump to read a single file from the snapshot
        $result = $this->agent->send('backup.read_file', [
            'username' => $this->username,
            'snapshot_id' => $this->snapshotId,
            'path' => $path,
        ]);

        if (!($result['success'] ?? false)) {
            throw new RuntimeException($result['error'] ?? 'Failed to read file');
        }

        return ['content' => $result['content'] ?? ''];
    }

    public function info(string $path): array
    {
        return ['info' => ['permissions' => 'r--r--r--']]; // Snapshots are read-only
    }

    // Write operations — not supported for snapshots
    public function write(string $path, string $content): array
    {
        throw new RuntimeException('Snapshots are read-only');
    }

    public function delete(string $path): array
    {
        throw new RuntimeException('Snapshots are read-only');
    }

    public function mkdir(string $path): array
    {
        throw new RuntimeException('Snapshots are read-only');
    }

    public function rename(string $oldPath, string $newPath): array
    {
        throw new RuntimeException('Snapshots are read-only');
    }

    public function copy(string $source, string $destination): array
    {
        throw new RuntimeException('Snapshots are read-only');
    }

    public function move(string $source, string $destination): array
    {
        throw new RuntimeException('Snapshots are read-only');
    }

    public function upload(string $directory, string $filename, string $content): array
    {
        throw new RuntimeException('Snapshots are read-only');
    }
}
```

### Agent side: backup.browse and backup.read_file

These agent handlers call `jabali-backup ls` and `jabali-backup dump`:

```php
// In /etc/jabali/agent.d/jabali-backup.php

function backupBrowse(array $params): array
{
    $username = $params['username'] ?? '';
    $snapshotId = $params['snapshot_id'] ?? 'latest';
    $path = $params['path'] ?? '/';

    if (empty($username) || !validateUsername($username)) {
        return ['success' => false, 'error' => 'Invalid username'];
    }
    if (!preg_match('/^[a-f0-9]+$|^latest$/', $snapshotId)) {
        return ['success' => false, 'error' => 'Invalid snapshot ID'];
    }

    $cmd = ['/usr/local/bin/jabali-backup', 'ls', $username,
            '--snapshot=' . $snapshotId, '--path=' . $path, '--json'];

    $proc = proc_open($cmd, [1 => ['pipe', 'w'], 2 => ['pipe', 'w']], $pipes);
    $stdout = stream_get_contents($pipes[1]);
    $stderr = stream_get_contents($pipes[2]);
    fclose($pipes[1]);
    fclose($pipes[2]);
    $exitCode = proc_close($proc);

    if ($exitCode !== 0) {
        return ['success' => false, 'error' => trim($stderr ?: 'Browse failed')];
    }

    $data = json_decode($stdout, true);

    // Transform to file browser format
    $items = [];
    foreach ($data ?? [] as $entry) {
        $items[] = [
            'name' => basename($entry['path'] ?? ''),
            'path' => $entry['path'] ?? '',
            'is_dir' => ($entry['type'] ?? '') === 'dir',
            'size' => $entry['size'] ?? 0,
            'modified' => strtotime($entry['mtime'] ?? 'now'),
            'permissions' => $entry['permissions'] ?? '-r--r--r--',
        ];
    }

    return ['success' => true, 'items' => $items];
}
```

### Using the adapter in your Backup page

You don't need to register the adapter globally. Use it on-demand in your restore/browse page:

```php
// In your Backups page when user clicks "Browse" on a snapshot:
public function browseSnapshot(string $snapshotId): void
{
    $user = auth()->user(); // or from the backup record

    $adapter = new ResticSnapshotAdapter(
        app(AgentClient::class),
        $snapshotId,
        $user->username,
    );

    // Now you can use $adapter->files()->list('/') to browse
    // Or embed the full FileBrowser page with a custom adapter binding
}
```

### Embedding as a full-page browser

To show the complete file browser UI for a snapshot, create a dedicated page that temporarily binds your adapter:

```php
<?php

declare(strict_types=1);

namespace App\Backup\Pages;

use App\Backup\Adapters\ResticSnapshotAdapter;
use App\FileBrowser\Adapters\FileBrowserAdapter;
use App\FileBrowser\Pages\FileBrowser;
use App\Services\Agent\AgentClient;

class SnapshotBrowser extends FileBrowser
{
    protected static ?string $slug = 'backups/browse/{snapshotId}';
    protected static bool $shouldRegisterNavigation = false;

    public ?string $snapshotId = null;
    public ?string $username = null;

    public function mount(?string $snapshotId = null): void
    {
        $this->snapshotId = $snapshotId ?? request()->route('snapshotId');

        // Look up snapshot from DB to get username
        // $snapshot = BackupSnapshot::findOrFail($this->snapshotId);
        // $this->username = $snapshot->username;

        // Bind the read-only snapshot adapter for this request
        app()->bind(FileBrowserAdapter::class, fn () => new ResticSnapshotAdapter(
            app(AgentClient::class),
            $this->snapshotId,
            $this->username,
        ));

        parent::mount();
    }

    public function getTitle(): string|\Illuminate\Contracts\Support\Htmlable
    {
        return __('Browse Snapshot');
    }
}
```

This gives you the full file browser UI (tree navigation, file preview, search) for free — just backed by restic snapshot data instead of live files. The write operations (upload, delete, rename) will show errors if attempted since the adapter throws on write ops.

### Disabling write features

The `FileBrowser` page now supports per-instance read-only mode. Set `$readOnly = true` in your subclass — this hides ALL write operations (upload, edit, rename, delete, trash, extract, permissions, new folder, bulk actions):

```php
class SnapshotBrowser extends FileBrowser
{
    protected bool $readOnly = true;  // Hides all write operations

    protected static ?string $slug = 'backups/browse/{snapshotId}';
    protected static bool $shouldRegisterNavigation = false;

    // ... mount(), adapter binding, etc.
}
```

For granular control, use `$disabledFeatures` instead:

```php
class SnapshotBrowser extends FileBrowser
{
    // Disable specific features — keeps the rest
    protected array $disabledFeatures = ['upload', 'trash', 'extract', 'permissions'];

    // This keeps: edit (view file contents), rename, new folder
}
```

**Do NOT use `FileBrowserPlugin::make()->upload(false)`** — that changes the global config and affects the main file browser too. Always use the per-instance properties above.

### Item format reference

Each item in the `list()` response must have this shape:

```php
[
    'name' => 'index.php',           // filename only
    'path' => 'domains/example.com/public_html/index.php',  // relative path
    'is_dir' => false,
    'size' => 1234,                  // bytes (null for dirs)
    'modified' => 1712345678,        // unix timestamp
    'permissions' => '-rw-r--r--',   // ls-style string
]
```

Directories should have `is_dir => true` and `size => null` or `0`.

## Tab Switch Skeleton Loading

When the user switches tabs (e.g. Backups → Schedules → Logs), Livewire re-renders the content. During that transition the UI can flash empty or show stale content. The panel solves this with a **skeleton loading animation** that shows pulsing bars while the new tab loads.

### How it works

Three pieces:

1. **Global script** (`resources/views/components/tab-skeleton-script.blade.php`) — auto-injected into both panels via `PanelsRenderHook::SCRIPTS_AFTER`. You don't need to include it manually.

2. **Blade wrapper** (`<x-tab-loading-skeleton>`) — wrap each tab's content view in this component.

3. **Livewire property** — your page must have a public `$activeTab` property that Livewire updates on tab switch. The skeleton script watches for `activeTab` changes in Livewire commits.

### How the skeleton script works

The script (already loaded globally) does this:

```
1. Livewire commit detected with 'activeTab' update
2. → Hide all content after the tab bar
3. → Show skeleton (pulsing gray bars)
4. Livewire morph completes
5. → Hide skeleton, show new content
```

It handles dark mode automatically (adjusts bar colors).

### Implementation

**Step 1: Page class** — use `$activeTab` with `#[Url]` for tab state:

```php
use Livewire\Attributes\Url;

class Backups extends Page implements HasTable
{
    #[Url(as: 'tab')]
    public ?string $activeTab = 'backups';

    public function updatedActiveTab(): void
    {
        $this->resetTable(); // Reset table pagination/sorting on tab switch
    }

    // Switch table query based on active tab
    public function table(Table $table): Table
    {
        return match ($this->activeTab) {
            'schedules' => $this->schedulesTable($table),
            'logs' => $this->logsTable($table),
            default => $this->backupsTable($table),
        };
    }
}
```

**Step 2: Main page Blade view** — use Filament Tabs with `wire:click`:

```blade
{{-- resources/views/filament/admin/pages/backups.blade.php --}}
<x-filament-panels::page>
    {{ $this->settingsForm }}

    <x-filament::tabs>
        <x-filament::tabs.item
            :active="$activeTab === 'backups'"
            wire:click="$set('activeTab', 'backups')"
            icon="heroicon-o-cloud-arrow-up"
        >
            {{ __('Backups') }}
        </x-filament::tabs.item>

        <x-filament::tabs.item
            :active="$activeTab === 'schedules'"
            wire:click="$set('activeTab', 'schedules')"
            icon="heroicon-o-clock"
        >
            {{ __('Schedules') }}
        </x-filament::tabs.item>

        <x-filament::tabs.item
            :active="$activeTab === 'logs'"
            wire:click="$set('activeTab', 'logs')"
            icon="heroicon-o-document-text"
        >
            {{ __('Logs') }}
        </x-filament::tabs.item>
    </x-filament::tabs>

    {{-- Tab content — skeleton shows automatically during switch --}}
    @if($activeTab === 'backups')
        @include('filament.admin.pages.backups-tab-content')
    @elseif($activeTab === 'schedules')
        @include('filament.admin.pages.backups-tab-schedules')
    @elseif($activeTab === 'logs')
        @include('filament.admin.pages.backups-tab-logs')
    @endif

    <x-filament-actions::modals />
</x-filament-panels::page>
```

**Step 3: Tab content partials** — wrap in `<x-tab-loading-skeleton>`:

```blade
{{-- resources/views/filament/admin/pages/backups-tab-content.blade.php --}}
<x-tab-loading-skeleton>
    <div class="space-y-6">
        <x-filament::section icon="heroicon-o-cloud-arrow-up">
            <x-slot name="heading">
                {{ __('Backup Snapshots') }}
            </x-slot>
            <x-slot name="headerEnd">
                <x-filament::badge color="info">{{ count($this->snapshots) }}</x-filament::badge>
            </x-slot>

            {{ $this->table }}
        </x-filament::section>
    </div>
</x-tab-loading-skeleton>
```

### What you get for free

- Pulsing gray skeleton bars during tab transitions (no flash of empty content)
- Dark mode support (auto-detects `dark` class on `<html>`)
- Works with any number of tabs
- No JavaScript to write — the global script handles everything
- URL updates with tab state (`?tab=schedules`) via `#[Url]`

### Sub-tabs (like Databases → PostgreSQL → Databases/Users)

If you have sub-tabs within a tab (e.g. Databases vs Users under PostgreSQL), use `wire:click` to call a method instead of `$set`:

```blade
<x-filament::button
    :color="$this->subTab === 'databases' ? 'primary' : 'gray'"
    wire:click="setSubTab('databases')"
    size="sm"
>
    {{ __('Databases') }}
</x-filament::button>
```

```php
public function setSubTab(string $tab): void
{
    $this->subTab = $tab;
    $this->resetTable();
}
```

Note: The skeleton script watches for `activeTab` and `viewMode` property changes. If your sub-tab uses a different property name (like `subTab`), the skeleton won't trigger automatically. You can either rename your property to `viewMode`, or the content will just swap without skeleton (still works, just no loading animation).
