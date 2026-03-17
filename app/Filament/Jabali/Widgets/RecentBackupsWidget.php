<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

use App\Models\Backup;
use App\Support\Formatter;
use Filament\Actions\Action;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Table;
use Filament\Widgets\TableWidget as BaseWidget;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Support\Facades\Auth;

class RecentBackupsWidget extends BaseWidget
{
    protected static ?int $sort = 5;

    protected int|string|array $columnSpan = 1;

    public function table(Table $table): Table
    {
        return $table
            ->query($this->getTableQuery())
            ->heading(__('Recent Backups'))
            ->description(__('Your latest backup activity'))
            ->columns([
                TextColumn::make('name')
                    ->label(__('Backup'))
                    ->icon('heroicon-o-archive-box')
                    ->weight('medium')
                    ->limit(30),

                TextColumn::make('size_bytes')
                    ->label(__('Size'))
                    ->formatStateUsing(fn ($state) => Formatter::bytes((int) $state))
                    ->badge()
                    ->color('gray'),

                IconColumn::make('status')
                    ->label(__('Status'))
                    ->icon(fn (string $state): string => match ($state) {
                        'completed' => 'heroicon-o-check-circle',
                        'failed' => 'heroicon-o-x-circle',
                        'running' => 'heroicon-o-arrow-path',
                        default => 'heroicon-o-clock',
                    })
                    ->color(fn (string $state): string => match ($state) {
                        'completed' => 'success',
                        'failed' => 'danger',
                        'running' => 'warning',
                        default => 'gray',
                    }),

                TextColumn::make('created_at')
                    ->label(__('Created'))
                    ->since()
                    ->dateTimeTooltip(),
            ])
            ->actions([
                Action::make('download')
                    ->label(__('Download'))
                    ->icon('heroicon-o-arrow-down-tray')
                    ->url(fn (Backup $record): string => route('filament.jabali.pages.backups', ['download' => $record->id]))
                    ->visible(fn (Backup $record): bool => $record->status === 'completed')
                    ->size('sm'),
            ])
            ->headerActions([
                Action::make('viewAll')
                    ->label(__('View All'))
                    ->url(route('filament.jabali.pages.backups'))
                    ->button()
                    ->size('sm'),
            ])
            ->emptyStateHeading(__('No backups yet'))
            ->emptyStateDescription(__('Create your first backup to protect your data.'))
            ->emptyStateIcon('heroicon-o-archive-box')
            ->emptyStateActions([
                Action::make('create')
                    ->label(__('Create Backup'))
                    ->url(route('filament.jabali.pages.backups'))
                    ->icon('heroicon-o-plus')
                    ->button(),
            ])
            ->paginated(false);
    }

    protected function getTableQuery(): Builder
    {
        return Backup::query()
            ->where('user_id', Auth::id())
            ->orderBy('created_at', 'desc')
            ->limit(5);
    }
}
