<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Services\Agent\AgentClient;
use App\Support\Formatter;
use App\Support\SafeError;
use BackedEnum;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Illuminate\Contracts\Support\Htmlable;

class UserBackups extends Page implements HasActions, HasForms
{
    use InteractsWithActions;
    use InteractsWithForms;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-cloud-arrow-up';

    protected static ?int $navigationSort = 16;

    protected string $view = 'filament.jabali.pages.backups';

    // ─── State ───

    public string $activeTab = 'snapshots';

    public array $snapshots = [];

    public string $browseSnapshotId = '';

    public string $browsePath = '';

    public array $browseItems = [];

    public array $selectedFiles = [];

    public string $restoreSnapshotId = '';

    public bool $restoreFiles = true;

    public bool $restoreDatabases = true;

    public bool $restoreMailboxes = true;

    public bool $restoreForce = false;

    public string $restoreOutput = '';

    public static function getNavigationLabel(): string
    {
        return __('Backups');
    }

    public function getTitle(): string|Htmlable
    {
        return __('Backups');
    }

    private function username(): string
    {
        return auth()->user()->username;
    }

    public function mount(): void
    {
        $this->loadSnapshots();
    }

    // ─── Header Actions ───

    protected function getHeaderActions(): array
    {
        return [
            Action::make('requestBackup')
                ->label(__('Create Backup'))
                ->icon('heroicon-o-cloud-arrow-up')
                ->color('primary')
                ->requiresConfirmation()
                ->modalHeading(__('Create Backup'))
                ->modalDescription(__('Start a backup of your account? This runs in the background.'))
                ->action(function (): void {
                    $this->createBackup();
                }),
            Action::make('refresh')
                ->label(__('Refresh'))
                ->icon('heroicon-o-arrow-path')
                ->color('gray')
                ->action(fn () => $this->loadSnapshots()),
        ];
    }

    // ─── Snapshots ───

    public function loadSnapshots(): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.list_snapshots', [
                'username' => $this->username(),
            ]);
            $this->snapshots = $result['snapshots'] ?? [];
        } catch (\Throwable $e) {
            $this->snapshots = [];
        }
    }

    public function createBackup(): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.run', [
                'username' => $this->username(),
            ]);
            if ($result['success'] ?? false) {
                Notification::make()
                    ->title(__('Backup started'))
                    ->body(__('Running in background. Refresh to see the new snapshot.'))
                    ->success()
                    ->send();
            } else {
                throw new \RuntimeException($result['error'] ?? 'Unknown error');
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Backup failed'))->body(SafeError::message($e))->danger()->send();
        }
    }

    // ─── Browse ───

    public function browseSnapshot(string $snapshotId): void
    {
        $this->browseSnapshotId = $snapshotId;
        $this->browsePath = '';
        $this->selectedFiles = [];
        $this->activeTab = 'browser';
        $this->loadBrowseItems();
    }

    public function navigateTo(string $path): void
    {
        $this->browsePath = $path;
        $this->loadBrowseItems();
    }

    public function navigateUp(): void
    {
        $parts = array_filter(explode('/', $this->browsePath));
        array_pop($parts);
        $this->browsePath = implode('/', $parts);
        $this->loadBrowseItems();
    }

    public function loadBrowseItems(): void
    {
        try {
            $result = app(AgentClient::class)->send('jb.browse', [
                'username' => $this->username(),
                'path' => $this->browsePath,
                'snapshot_id' => $this->browseSnapshotId,
            ]);
            $this->browseItems = $result['items'] ?? [];
        } catch (\Throwable $e) {
            $this->browseItems = [];
        }
    }

    public function toggleFileSelection(string $path): void
    {
        if (in_array($path, $this->selectedFiles, true)) {
            $this->selectedFiles = array_values(array_diff($this->selectedFiles, [$path]));
        } else {
            $this->selectedFiles[] = $path;
        }
    }

    public function restoreSelectedFiles(): void
    {
        if (empty($this->selectedFiles)) {
            Notification::make()->title(__('No files selected'))->warning()->send();

            return;
        }

        try {
            $result = app(AgentClient::class)->send('jb.restore_files', [
                'username' => $this->username(),
                'snapshot_id' => $this->browseSnapshotId,
                'files' => $this->selectedFiles,
            ]);
            if ($result['success'] ?? false) {
                Notification::make()
                    ->title(__('Files restored'))
                    ->body(__(':count item(s) restored', ['count' => count($this->selectedFiles)]))
                    ->success()
                    ->send();
                $this->selectedFiles = [];
            } else {
                throw new \RuntimeException($result['error'] ?? 'Unknown error');
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Restore failed'))->body(SafeError::message($e))->danger()->send();
        }
    }

    // ─── Restore ───

    public function openRestore(string $snapshotId): void
    {
        $this->restoreSnapshotId = $snapshotId;
        $this->restoreFiles = true;
        $this->restoreDatabases = true;
        $this->restoreMailboxes = true;
        $this->restoreForce = false;
        $this->restoreOutput = '';
        $this->activeTab = 'restore';
    }

    public function executeRestore(): void
    {
        try {
            $components = [];
            if ($this->restoreFiles) {
                $components[] = 'files';
            }
            if ($this->restoreDatabases) {
                $components[] = 'mysql';
            }
            if ($this->restoreMailboxes) {
                $components[] = 'email';
            }

            $result = app(AgentClient::class)->send('jb.restore', [
                'username' => $this->username(),
                'snapshot_id' => $this->restoreSnapshotId,
                'components' => $components,
                'force' => $this->restoreForce,
            ]);
            if ($result['success'] ?? false) {
                $this->restoreOutput = $result['output'] ?? __('Restore completed.');
                Notification::make()->title(__('Restore completed'))->success()->send();
            } else {
                throw new \RuntimeException($result['error'] ?? 'Unknown error');
            }
        } catch (\Throwable $e) {
            $this->restoreOutput = $e->getMessage();
            Notification::make()->title(__('Restore failed'))->body(SafeError::message($e))->danger()->send();
        }
    }

    // ─── Helpers ───

    public function formatBytes(int|float|null $bytes): string
    {
        return Formatter::bytes($bytes ?? 0);
    }

    public function getFileBreadcrumbs(): array
    {
        $crumbs = [['label' => $this->username(), 'path' => '']];
        $parts = array_filter(explode('/', $this->browsePath));
        $accumulated = '';

        foreach ($parts as $part) {
            $accumulated .= ($accumulated ? '/' : '') . $part;
            $crumbs[] = ['label' => $part, 'path' => $accumulated];
        }

        return $crumbs;
    }
}
