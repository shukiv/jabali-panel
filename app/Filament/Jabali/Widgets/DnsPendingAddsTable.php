<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

use Filament\Actions\Action;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Support\Contracts\TranslatableContentDriver;
use Filament\Support\Enums\FontFamily;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Attributes\Reactive;
use Livewire\Component;

class DnsPendingAddsTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    #[Reactive]
    public array $records = [];

    public function makeFilamentTranslatableContentDriver(): ?TranslatableContentDriver
    {
        return null;
    }

    /**
     * @return array<int, array<string, mixed>>
     */
    protected function getRecords(): array
    {
        return collect($this->records)->values()->all();
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->getRecords())
            ->columns([
                TextColumn::make('type')
                    ->label(__('Type'))
                    ->badge()
                    ->color('success'),
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->fontFamily(FontFamily::Mono),
                TextColumn::make('content')
                    ->label(__('Content'))
                    ->fontFamily(FontFamily::Mono)
                    ->limit(50)
                    ->tooltip(fn (array $record): string => (string) ($record['content'] ?? '')),
                TextColumn::make('ttl')
                    ->label(__('TTL')),
                TextColumn::make('priority')
                    ->label(__('Priority'))
                    ->placeholder(__('-')),
            ])
            ->actions([
                Action::make('removePending')
                    ->label(__('Remove'))
                    ->icon('heroicon-o-x-mark')
                    ->color('danger')
                    ->action(function (array $record): void {
                        $key = $record['key'] ?? null;
                        if (! $key) {
                            return;
                        }

                        $this->dispatch('dns-pending-add-remove', key: $key);
                    }),
            ])
            ->striped()
            ->paginated(false)
            ->emptyStateHeading(__('No pending records'))
            ->emptyStateDescription(__('Queued records will appear here.'))
            ->emptyStateIcon('heroicon-o-plus-circle')
            ->poll(null);
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
