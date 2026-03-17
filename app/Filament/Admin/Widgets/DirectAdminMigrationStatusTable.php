<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Models\ServerImport;
use App\Models\ServerImportAccount;
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
use Livewire\Attributes\On;
use Livewire\Component;

class DirectAdminMigrationStatusTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public ?int $importId = null;

    public function mount(?int $importId = null): void
    {
        $this->importId = $importId ?: session('directadmin_migration.import_id');
    }

    #[On('directadmin-selection-updated')]
    public function refreshStatus(): void
    {
        $this->resetTable();
    }

    public function makeFilamentTranslatableContentDriver(): ?TranslatableContentDriver
    {
        return null;
    }

    protected function getImport(): ?ServerImport
    {
        if (! $this->importId) {
            return null;
        }

        return ServerImport::find($this->importId);
    }

    /**
     * @return array<int>
     */
    protected function getSelectedAccountIds(): array
    {
        $selected = $this->getImport()?->selected_accounts ?? [];

        return array_values(array_filter(array_map('intval', is_array($selected) ? $selected : [])));
    }

    /**
     * @return \Illuminate\Support\Collection<int, ServerImportAccount>
     */
    protected function getRecords()
    {
        if (! $this->importId) {
            return collect();
        }

        $ids = $this->getSelectedAccountIds();
        if ($ids === []) {
            return collect();
        }

        return ServerImportAccount::query()
            ->where('server_import_id', $this->importId)
            ->whereIn('id', $ids)
            ->orderBy('source_username')
            ->get();
    }

    protected function shouldPoll(): bool
    {
        $import = $this->getImport();
        if (! $import) {
            return false;
        }

        if (in_array($import->status, ['discovering', 'importing'], true)) {
            return true;
        }

        foreach ($this->getRecords() as $record) {
            if (! in_array($record->status, ['completed', 'failed', 'skipped'], true)) {
                return true;
            }
        }

        return false;
    }

    protected function getStatusText(string $status): string
    {
        return match ($status) {
            'pending' => __('Waiting...'),
            'importing' => __('Importing...'),
            'completed' => __('Completed'),
            'failed' => __('Failed'),
            'skipped' => __('Skipped'),
            default => __('Unknown'),
        };
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->getRecords())
            ->columns([
                IconColumn::make('status_icon')
                    ->label('')
                    ->icon(fn (ServerImportAccount $record): string => match ($record->status) {
                        'pending' => 'heroicon-o-clock',
                        'importing' => 'heroicon-o-arrow-path',
                        'completed' => 'heroicon-o-check-circle',
                        'failed' => 'heroicon-o-x-circle',
                        'skipped' => 'heroicon-o-minus-circle',
                        default => 'heroicon-o-question-mark-circle',
                    })
                    ->color(fn (ServerImportAccount $record): string => match ($record->status) {
                        'pending' => 'gray',
                        'importing' => 'warning',
                        'completed' => 'success',
                        'failed' => 'danger',
                        'skipped' => 'gray',
                        default => 'gray',
                    })
                    ->size(IconSize::Small)
                    ->extraAttributes(fn (ServerImportAccount $record): array => $record->status === 'importing'
                        ? ['class' => 'animate-spin']
                        : []),
                TextColumn::make('source_username')
                    ->label(__('Account'))
                    ->weight(FontWeight::Bold)
                    ->searchable(),
                TextColumn::make('status')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn (string $state): string => $this->getStatusText($state))
                    ->color(fn (ServerImportAccount $record): string => match ($record->status) {
                        'pending' => 'gray',
                        'importing' => 'warning',
                        'completed' => 'success',
                        'failed' => 'danger',
                        'skipped' => 'gray',
                        default => 'gray',
                    }),
                TextColumn::make('current_task')
                    ->label(__('Current Task'))
                    ->wrap()
                    ->limit(80)
                    ->default(__('Waiting...')),
                TextColumn::make('progress')
                    ->label(__('Progress'))
                    ->suffix('%')
                    ->toggleable(),
            ])
            ->striped()
            ->paginated(false)
            ->poll($this->shouldPoll() ? '3s' : null)
            ->emptyStateHeading(__('No selected accounts'))
            ->emptyStateDescription(__('Select accounts and start migration.'))
            ->emptyStateIcon('heroicon-o-queue-list');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
