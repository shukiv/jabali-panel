<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages;

use App\Backup\Adapters\ResticSnapshotAdapter;
use App\FileBrowser\Adapters\FileBrowserAdapter;
use App\FileBrowser\Pages\FileBrowser;
use App\Services\Agent\AgentClient;
use App\Support\SafeError;
use BackedEnum;
use Filament\Actions\BulkAction;
use Filament\Notifications\Notification;
use Filament\Tables\Table;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Collection;

class SnapshotBrowser extends FileBrowser
{
    protected bool $readOnly = true;

    protected static ?string $slug = 'backups-browse';

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-folder-open';

    protected static bool $shouldRegisterNavigation = false;

    public ?string $snapshotId = null;

    public ?string $snapshotUsername = null;

    public function mount(): void
    {
        $this->snapshotId = request()->query('snapshot', '');
        $this->snapshotUsername = request()->query('user', '');

        if (empty($this->snapshotId) || empty($this->snapshotUsername)) {
            abort(404);
        }
        if (! preg_match('/^[a-z][a-z0-9_-]{0,31}$/', $this->snapshotUsername)) {
            abort(404);
        }
        if ($this->snapshotId !== 'latest' && ! preg_match('/^[a-f0-9]{6,64}$/', $this->snapshotId)) {
            abort(404);
        }

        parent::mount();
    }

    public function getAdapter(): FileBrowserAdapter
    {
        return new ResticSnapshotAdapter(
            app(AgentClient::class),
            $this->snapshotId,
            $this->snapshotUsername,
        );
    }

    public function table(Table $table): Table
    {
        return parent::table($table)
            ->bulkActions([
                BulkAction::make('restoreSelected')
                    ->label(__('Restore Selected'))
                    ->icon('heroicon-o-arrow-path')
                    ->color('primary')
                    ->size('sm')
                    ->requiresConfirmation()
                    ->modalHeading(__('Restore Files'))
                    ->modalDescription(__('Restore the selected files and folders from this snapshot to the live server?'))
                    ->modalIcon('heroicon-o-arrow-path')
                    ->modalIconColor('warning')
                    ->modalSubmitActionLabel(__('Restore'))
                    ->deselectRecordsAfterCompletion()
                    ->action(function (Collection $records): void {
                        $files = $records
                            ->map(fn (array $r) => $r['path'] ?? '')
                            ->filter(fn (string $p) => $p !== '')
                            ->values()
                            ->all();

                        if (empty($files)) {
                            Notification::make()->title(__('No files selected'))->warning()->send();

                            return;
                        }

                        try {
                            $result = app(AgentClient::class)->send('jb.restore_files', [
                                'username' => $this->snapshotUsername,
                                'snapshot_id' => $this->snapshotId,
                                'files' => $files,
                            ]);

                            if ($result['success'] ?? false) {
                                Notification::make()
                                    ->title(__('Files restored'))
                                    ->body(__(':count item(s) restored', ['count' => count($files)]))
                                    ->success()
                                    ->send();
                            } else {
                                Notification::make()
                                    ->title(__('Restore failed'))
                                    ->body($result['error'] ?? __('Unknown error'))
                                    ->danger()
                                    ->send();
                            }
                        } catch (\Throwable $e) {
                            Notification::make()
                                ->title(__('Restore failed'))
                                ->body(SafeError::message($e))
                                ->danger()
                                ->send();
                        }
                    }),
            ]);
    }

    public function getTitle(): string|Htmlable
    {
        return __('Browse Snapshot :id — :user', [
            'id' => $this->snapshotId ?? '',
            'user' => $this->snapshotUsername ?? '',
        ]);
    }

    public static function getSlug(?\Filament\Panel $panel = null): string
    {
        return 'backups-browse';
    }

    public static function getNavigationLabel(): string
    {
        return __('Browse Snapshot');
    }

}
