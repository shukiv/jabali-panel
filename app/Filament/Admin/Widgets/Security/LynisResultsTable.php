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

class LynisResultsTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public array $results = [];

    public string $type = 'warnings'; // 'warnings' or 'suggestions'

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    protected function getRecords(): array
    {
        $items = $this->results[$this->type] ?? [];

        return array_map(fn ($item, $index) => [
            'id' => $index,
            'message' => $item,
        ], $items, array_keys($items));
    }

    public function table(Table $table): Table
    {
        $icon = $this->type === 'warnings' ? 'heroicon-o-exclamation-triangle' : 'heroicon-o-light-bulb';
        $color = $this->type === 'warnings' ? 'warning' : 'info';

        return $table
            ->records(fn () => $this->getRecords())
            ->columns([
                TextColumn::make('message')
                    ->label($this->type === 'warnings' ? __('Warning') : __('Suggestion'))
                    ->icon($icon)
                    ->iconColor($color)
                    ->wrap()
                    ->searchable(),
            ])
            ->striped()
            ->paginated([10, 25, 50])
            ->emptyStateHeading($this->type === 'warnings' ? __('No warnings') : __('No suggestions'))
            ->emptyStateDescription(__('The system audit found no issues in this category.'))
            ->emptyStateIcon('heroicon-o-check-circle');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
