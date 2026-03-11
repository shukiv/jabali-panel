<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Models\ServerImport;
use App\Models\ServerImportAccount;
use App\Models\User;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Contracts\TranslatableContentDriver;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Columns\TextInputColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Attributes\On;
use Livewire\Component;

class HestiaCpAccountConfigTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public ?int $importId = null;

    public function mount(?int $importId = null): void
    {
        $this->importId = $importId ?: session('hestiacp_migration.import_id');
    }

    #[On('hestiacp-config-updated')]
    #[On('hestiacp-selection-updated')]
    public function refreshConfig(): void
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

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->getRecords())
            ->columns([
                IconColumn::make('target_user_exists')
                    ->label(__('User'))
                    ->boolean()
                    ->trueIcon('heroicon-o-exclamation-triangle')
                    ->falseIcon('heroicon-o-user-plus')
                    ->trueColor('warning')
                    ->falseColor('success')
                    ->tooltip(fn (ServerImportAccount $record): string => User::where('username', $record->target_username)->exists()
                        ? __('User exists - migration will restore into the existing account')
                        : __('New user will be created'))
                    ->getStateUsing(fn (ServerImportAccount $record): bool => User::where('username', $record->target_username)->exists()),
                TextColumn::make('source_username')
                    ->label(__('Source'))
                    ->weight('bold'),
                TextColumn::make('main_domain')
                    ->label(__('Main Domain'))
                    ->wrap(),
                TextInputColumn::make('target_username')
                    ->label(__('Target Username'))
                    ->rules([
                        'required',
                        'max:32',
                        'regex:/^[a-z0-9_]+$/i',
                    ]),
                TextInputColumn::make('email')
                    ->label(__('Email'))
                    ->rules([
                        'nullable',
                        'email',
                        'max:255',
                    ]),
                TextColumn::make('formatted_disk_usage')
                    ->label(__('Disk'))
                    ->toggleable(),
            ])
            ->striped()
            ->paginated([10, 25, 50])
            ->defaultPaginationPageOption(10)
            ->emptyStateHeading(__('No accounts selected'))
            ->emptyStateDescription(__('Go back and select accounts to migrate.'))
            ->emptyStateIcon('heroicon-o-user-group');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
