<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets\Security;

use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Component;

class ThreatsTable extends Component implements HasTable, HasSchemas, HasActions
{
    use InteractsWithTable;
    use InteractsWithSchemas;
    use InteractsWithActions;

    public array $threats = [];

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => array_reverse($this->threats))
            ->columns([
                TextColumn::make('threat')
                    ->label(__('Threat'))
                    ->icon('heroicon-o-exclamation-triangle')
                    ->iconColor('danger')
                    ->badge()
                    ->color('danger')
                    ->searchable(),
                TextColumn::make('file')
                    ->label(__('File Path'))
                    ->formatStateUsing(fn (?string $state): string => $state ? basename($state) : '-')
                    ->tooltip(fn (array $record): string => $record['file'] ?? '')
                    ->fontFamily('mono')
                    ->limit(50),
            ])
            ->striped()
            ->emptyStateHeading(__('No threats detected'))
            ->emptyStateDescription(__('No recent threats have been found.'))
            ->emptyStateIcon('heroicon-o-check-circle');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
