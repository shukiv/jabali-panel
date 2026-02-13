<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Contracts\TranslatableContentDriver;
use Filament\Support\Enums\FontWeight;
use Filament\Support\Enums\IconSize;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Support\Facades\Cache;
use Livewire\Attributes\On;
use Livewire\Component;

class WhmMigrationStatusTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public array $selectedAccounts = [];

    public array $migrationStatus = [];

    public bool $isMigrating = false;

    public ?string $migrationCacheKey = null;

    public function mount(array $selectedAccounts = [], array $migrationStatus = [], bool $isMigrating = false, ?string $migrationCacheKey = null): void
    {
        $this->selectedAccounts = ! empty($selectedAccounts) ? $selectedAccounts : session('whm_migration.selectedAccounts', []);
        $this->migrationStatus = ! empty($migrationStatus) ? $migrationStatus : session('whm_migration.migrationStatus', []);
        $this->isMigrating = $isMigrating;
        $this->migrationCacheKey = $migrationCacheKey;
        $this->loadStateFromCache();
    }

    #[On('whm-migration-status-updated')]
    public function refreshStatus(): void
    {
        $this->loadStateFromCache();
        $this->resetTable();
    }

    public function makeFilamentTranslatableContentDriver(): ?TranslatableContentDriver
    {
        return null;
    }

    protected function getRecords(): array
    {
        $this->loadStateFromCache();

        $records = [];

        foreach ($this->selectedAccounts as $user) {
            $status = $this->migrationStatus[$user] ?? ['status' => 'pending', 'log' => []];
            $lastLog = end($status['log']) ?: null;

            $records[] = [
                'user' => $user,
                'status' => $status['status'] ?? 'pending',
                'status_text' => $this->getStatusText($status['status'] ?? 'pending'),
                'message' => $lastLog ? $lastLog['message'] : $this->getStatusText($status['status'] ?? 'pending'),
                'progress' => $status['progress'] ?? 0,
                'log_count' => count($status['log'] ?? []),
            ];
        }

        return $records;
    }

    protected function getStatusText(string $status): string
    {
        return match ($status) {
            'pending' => __('Waiting...'),
            'processing' => __('Processing...'),
            'backup_creating' => __('Creating backup...'),
            'backup_downloading' => __('Downloading backup...'),
            'restoring' => __('Restoring...'),
            'completed' => __('Completed'),
            'error' => __('Error'),
            default => __('Unknown'),
        };
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->getRecords())
            ->columns([
                IconColumn::make('status')
                    ->label('')
                    ->icon(fn (array $record): string => match ($record['status']) {
                        'pending' => 'heroicon-o-clock',
                        'processing', 'backup_creating', 'backup_downloading', 'restoring' => 'heroicon-o-arrow-path',
                        'completed' => 'heroicon-o-check-circle',
                        'error' => 'heroicon-o-x-circle',
                        default => 'heroicon-o-question-mark-circle',
                    })
                    ->color(fn (array $record): string => match ($record['status']) {
                        'pending' => 'gray',
                        'processing', 'backup_creating', 'backup_downloading', 'restoring' => 'warning',
                        'completed' => 'success',
                        'error' => 'danger',
                        default => 'gray',
                    })
                    ->size(IconSize::Small)
                    ->extraAttributes(fn (array $record): array => in_array($record['status'], ['processing', 'backup_creating', 'backup_downloading', 'restoring'])
                        ? ['class' => 'animate-spin']
                        : []),
                TextColumn::make('user')
                    ->label(__('Account'))
                    ->weight(FontWeight::Bold)
                    ->searchable(),
                TextColumn::make('status_text')
                    ->label(__('Status'))
                    ->badge()
                    ->color(fn (array $record): string => match ($record['status']) {
                        'pending' => 'gray',
                        'processing', 'backup_creating', 'backup_downloading', 'restoring' => 'warning',
                        'completed' => 'success',
                        'error' => 'danger',
                        default => 'gray',
                    }),
                TextColumn::make('message')
                    ->label(__('Current Action'))
                    ->wrap()
                    ->limit(50),
            ])
            ->striped()
            ->paginated(false)
            ->poll($this->shouldPoll() ? '3s' : null)
            ->emptyStateHeading(__('No accounts in migration'))
            ->emptyStateDescription(__('Select accounts and start migration'))
            ->emptyStateIcon('heroicon-o-queue-list');
    }

    protected function shouldPoll(): bool
    {
        if ($this->isMigrating) {
            return true;
        }

        foreach ($this->migrationStatus as $status) {
            if (! in_array($status['status'] ?? null, ['completed', 'error'], true)) {
                return true;
            }
        }

        return false;
    }

    protected function loadStateFromCache(): void
    {
        if (! $this->migrationCacheKey) {
            return;
        }

        $state = Cache::get($this->migrationCacheKey);
        if (! is_array($state)) {
            return;
        }

        $this->selectedAccounts = $state['selectedAccounts'] ?? $this->selectedAccounts;
        $this->migrationStatus = $state['migrationStatus'] ?? $this->migrationStatus;
        $this->isMigrating = (bool) ($state['isMigrating'] ?? $this->isMigrating);
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
