<?php

declare(strict_types=1);

namespace App\Backup\Concerns;

use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use Filament\Notifications\Notification;

/**
 * Shared browse/navigate/restore-files logic for Backups and UserBackups pages.
 *
 * Expects the using class to declare these public properties:
 *   string $browseSnapshotId, string $browsePath, array $browseItems, array $selectedFiles
 *
 * And to implement browseUsername(): string (returns the account being browsed).
 */
trait BrowsesSnapshots
{
    public function navigateTo(string $path): void
    {
        $path = self::sanitizeBrowsePath($path);
        if ($path === null) {
            return;
        }
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
                'username' => $this->browseUsername(),
                'path' => $this->browsePath,
                'snapshot_id' => $this->browseSnapshotId,
            ]);
            $this->browseItems = $result['items'] ?? [];
        } catch (\Throwable $e) {
            $this->browseItems = [];
            Notification::make()->title(__('Browse failed'))->body(SafeError::message($e))->danger()->send();
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
                'username' => $this->browseUsername(),
                'snapshot_id' => $this->browseSnapshotId,
                'files' => $this->selectedFiles,
            ]);
            if ($result['success'] ?? false) {
                Notification::make()->title(__('Files restored'))
                    ->body(__(':count item(s) restored', ['count' => count($this->selectedFiles)]))->success()->send();
                $this->selectedFiles = [];
            } else {
                Notification::make()->title(__('Error'))
                    ->body(SafeError::fromAgent($result['error'] ?? __('Unknown error')))->danger()->send();
            }
        } catch (\Throwable $e) {
            Notification::make()->title(__('Restore failed'))->body(SafeError::message($e))->danger()->send();
        }
    }

    public function getFileBreadcrumbs(): array
    {
        $crumbs = [['label' => $this->browseUsername(), 'path' => '']];
        $parts = array_filter(explode('/', $this->browsePath));
        $accumulated = '';

        foreach ($parts as $part) {
            $accumulated .= ($accumulated ? '/' : '') . $part;
            $crumbs[] = ['label' => $part, 'path' => $accumulated];
        }

        return $crumbs;
    }

    private static function sanitizeBrowsePath(string $path): ?string
    {
        if (str_contains($path, '..') || str_contains($path, "\0")) {
            return null;
        }
        if (! preg_match('#^[a-zA-Z0-9/_\-.\s]*$#', $path)) {
            return null;
        }

        return $path;
    }
}
