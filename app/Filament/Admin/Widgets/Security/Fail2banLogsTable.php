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

class Fail2banLogsTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public array $logs = [];

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->logs)
            ->columns([
                TextColumn::make('time')
                    ->label(__('Time'))
                    ->fontFamily('mono')
                    ->sortable(),
                TextColumn::make('jail')
                    ->label(__('Jail'))
                    ->badge()
                    ->color('gray'),
                TextColumn::make('action')
                    ->label(__('Action'))
                    ->badge()
                    ->color(fn (string $state): string => match (strtolower($state)) {
                        'ban' => 'danger',
                        'unban' => 'success',
                        'found' => 'warning',
                        default => 'gray',
                    }),
                TextColumn::make('ip')
                    ->label(__('IP'))
                    ->fontFamily('mono')
                    ->copyable()
                    ->placeholder(__('-')),
                TextColumn::make('message')
                    ->label(__('Message'))
                    ->wrap(),
            ])
            ->emptyStateHeading(__('No log entries'))
            ->emptyStateDescription(__('Fail2ban logs will appear here once activity is detected.'))
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
