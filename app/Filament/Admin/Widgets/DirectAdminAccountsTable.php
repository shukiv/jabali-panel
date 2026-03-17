<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

use App\Models\ServerImport;
use App\Models\ServerImportAccount;
use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Contracts\TranslatableContentDriver;
use Filament\Support\Enums\IconSize;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Attributes\On;
use Livewire\Component;

class DirectAdminAccountsTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public ?int $importId = null;

    public function mount(?int $importId = null): void
    {
        $this->importId = $importId ?: session('directadmin_migration.import_id');
    }

    #[On('directadmin-accounts-updated')]
    #[On('directadmin-selection-updated')]
    public function refreshAccounts(): void
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

        return ServerImportAccount::query()
            ->where('server_import_id', $this->importId)
            ->orderBy('source_username')
            ->get();
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->getRecords())
            ->columns([
                IconColumn::make('is_selected')
                    ->label('')
                    ->boolean()
                    ->trueIcon('heroicon-s-check-circle')
                    ->falseIcon('heroicon-o-minus-circle')
                    ->trueColor('primary')
                    ->falseColor('gray')
                    ->size(IconSize::Medium)
                    ->getStateUsing(fn (ServerImportAccount $record): bool => in_array($record->id, $this->getSelectedAccountIds(), true)),
                TextColumn::make('source_username')
                    ->label(__('Username'))
                    ->weight('bold')
                    ->searchable(),
                TextColumn::make('main_domain')
                    ->label(__('Main Domain'))
                    ->wrap()
                    ->searchable(),
                TextColumn::make('email')
                    ->label(__('Email'))
                    ->icon('heroicon-o-envelope')
                    ->toggleable()
                    ->wrap(),
                TextColumn::make('formatted_disk_usage')
                    ->label(__('Disk'))
                    ->toggleable(),
            ])
            ->recordAction('toggleSelection')
            ->actions([
                Action::make('toggleSelection')
                    ->label(fn (ServerImportAccount $record): string => in_array($record->id, $this->getSelectedAccountIds(), true) ? __('Deselect') : __('Select'))
                    ->icon(fn (ServerImportAccount $record): string => in_array($record->id, $this->getSelectedAccountIds(), true) ? 'heroicon-o-x-mark' : 'heroicon-o-check')
                    ->color(fn (ServerImportAccount $record): string => in_array($record->id, $this->getSelectedAccountIds(), true) ? 'gray' : 'primary')
                    ->action(function (ServerImportAccount $record): void {
                        $import = $this->getImport();
                        if (! $import) {
                            return;
                        }

                        $selected = $this->getSelectedAccountIds();

                        if (in_array($record->id, $selected, true)) {
                            $selected = array_values(array_diff($selected, [$record->id]));
                        } else {
                            $selected[] = $record->id;
                            $selected = array_values(array_unique($selected));
                        }

                        $import->update(['selected_accounts' => $selected]);

                        $this->dispatch('directadmin-selection-updated');
                        $this->resetTable();
                    }),
            ])
            ->striped()
            ->paginated([10, 25, 50])
            ->defaultPaginationPageOption(25)
            ->emptyStateHeading(__('No accounts found'))
            ->emptyStateDescription(__('Discover accounts to see them here.'))
            ->emptyStateIcon('heroicon-o-user-group')
            ->poll(null);
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
