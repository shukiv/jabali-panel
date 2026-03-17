<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets\Security;

use App\Models\AuditLog;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Livewire\Component;

class AuditLogsTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(AuditLog::query()->with('user')->latest())
            ->columns([
                TextColumn::make('action')
                    ->label(__('Action'))
                    ->badge()
                    ->color(fn (string $state): string => match ($state) {
                        'login', 'logout' => 'info',
                        'create', 'created' => 'success',
                        'delete', 'deleted' => 'danger',
                        'update', 'updated' => 'warning',
                        default => 'gray',
                    })
                    ->searchable(),
                TextColumn::make('category')
                    ->label(__('Category'))
                    ->badge()
                    ->color('gray')
                    ->searchable(),
                TextColumn::make('description')
                    ->label(__('Description'))
                    ->limit(50)
                    ->tooltip(fn ($record) => $record->description)
                    ->searchable(),
                TextColumn::make('user.name')
                    ->label(__('User'))
                    ->default(__('System'))
                    ->searchable(),
                TextColumn::make('ip_address')
                    ->label(__('IP'))
                    ->fontFamily('mono')
                    ->size('xs')
                    ->color('gray')
                    ->searchable(),
                TextColumn::make('created_at')
                    ->label(__('Time'))
                    ->since()
                    ->sortable(),
            ])
            ->defaultSort('created_at', 'desc')
            ->striped()
            ->paginated([10, 25, 50, 100])
            ->defaultPaginationPageOption(10)
            ->emptyStateHeading(__('No audit logs'))
            ->emptyStateDescription(__('No actions have been logged yet.'))
            ->emptyStateIcon('heroicon-o-clipboard-document-list');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
