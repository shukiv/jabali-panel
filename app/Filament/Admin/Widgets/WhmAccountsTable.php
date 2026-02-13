<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets;

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

class WhmAccountsTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public array $accounts = [];

    public array $selectedAccounts = [];

    public function mount(array $accounts = [], array $selectedAccounts = []): void
    {
        // Use mount parameters if provided, otherwise load from session
        $this->accounts = ! empty($accounts) ? $accounts : session('whm_migration.accounts', []);
        $this->selectedAccounts = ! empty($selectedAccounts) ? $selectedAccounts : session('whm_migration.selectedAccounts', []);
    }

    #[On('whm-accounts-updated')]
    public function refreshAccounts(): void
    {
        // Reload from session since parent may have updated
        $this->accounts = session('whm_migration.accounts', []);
        $this->selectedAccounts = session('whm_migration.selectedAccounts', []);
        $this->resetTable();
    }

    public function makeFilamentTranslatableContentDriver(): ?TranslatableContentDriver
    {
        return null;
    }

    protected function getRecords(): array
    {
        $collection = collect($this->accounts)->map(function ($account) {
            $account['is_selected'] = in_array($account['user'] ?? '', $this->selectedAccounts);

            return $account;
        });

        // Get sort column and direction from table state
        $sortColumn = $this->getTableSortColumn();
        $sortDirection = $this->getTableSortDirection() ?? 'asc';

        if ($sortColumn) {
            $collection = $sortDirection === 'desc'
                ? $collection->sortByDesc($sortColumn)
                : $collection->sortBy($sortColumn);
        }

        return $collection->values()->all();
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
                    ->size(IconSize::Medium),
                TextColumn::make('user')
                    ->label(__('Username'))
                    ->weight('bold')
                    ->sortable(query: fn ($query, string $direction) => $query),
                TextColumn::make('domain')
                    ->label(__('Domain'))
                    ->wrap()
                    ->sortable(query: fn ($query, string $direction) => $query),
                TextColumn::make('diskused')
                    ->label(__('Disk'))
                    ->toggleable()
                    ->sortable(query: fn ($query, string $direction) => $query),
                TextColumn::make('plan')
                    ->label(__('Plan'))
                    ->toggleable()
                    ->limit(20)
                    ->sortable(query: fn ($query, string $direction) => $query),
                IconColumn::make('suspended')
                    ->label(__('Status'))
                    ->boolean()
                    ->trueIcon('heroicon-o-exclamation-triangle')
                    ->falseIcon('heroicon-o-check-circle')
                    ->trueColor('warning')
                    ->falseColor('success')
                    ->getStateUsing(fn (array $record): bool => $record['suspended'] ?? false),
            ])
            ->defaultSort('user')
            ->recordAction('toggleSelection')
            ->actions([
                Action::make('toggleSelection')
                    ->label(fn (array $record): string => in_array($record['user'] ?? '', $this->selectedAccounts) ? __('Deselect') : __('Select'))
                    ->icon(fn (array $record): string => in_array($record['user'] ?? '', $this->selectedAccounts) ? 'heroicon-o-x-mark' : 'heroicon-o-check')
                    ->color(fn (array $record): string => in_array($record['user'] ?? '', $this->selectedAccounts) ? 'gray' : 'primary')
                    ->action(function (array $record): void {
                        $user = $record['user'] ?? '';
                        if (in_array($user, $this->selectedAccounts)) {
                            $this->selectedAccounts = array_values(array_diff($this->selectedAccounts, [$user]));
                        } else {
                            $this->selectedAccounts[] = $user;
                        }
                        // Update session
                        session()->put('whm_migration.selectedAccounts', $this->selectedAccounts);
                        $this->dispatch('whm-selection-updated', selectedAccounts: $this->selectedAccounts);
                        $this->resetTable();
                    }),
            ])
            ->striped()
            ->paginated([10, 25, 50])
            ->defaultPaginationPageOption(25)
            ->emptyStateHeading(__('No accounts found'))
            ->emptyStateDescription(__('Connect to a WHM server to see available accounts'))
            ->emptyStateIcon('heroicon-o-user-group')
            ->poll(null);
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
