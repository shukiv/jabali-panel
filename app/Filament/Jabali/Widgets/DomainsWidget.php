<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

use App\Models\Domain;
use Filament\Actions\Action;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Table;
use Filament\Widgets\TableWidget as BaseWidget;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Support\Facades\Auth;

class DomainsWidget extends BaseWidget
{
    protected static ?int $sort = 2;

    protected int|string|array $columnSpan = 1;

    public function table(Table $table): Table
    {
        return $table
            ->query($this->getTableQuery())
            ->heading(__('Your Domains'))
            ->description(__('Recent domains in your account'))
            ->columns([
                TextColumn::make('domain')
                    ->label(__('Domain'))
                    ->icon('heroicon-o-globe-alt')
                    ->url(fn (Domain $record): string => "https://{$record->domain}")
                    ->openUrlInNewTab()
                    ->weight('medium'),

                IconColumn::make('ssl_active')
                    ->label(__('SSL'))
                    ->state(fn (Domain $record): bool => $record->hasSslActive())
                    ->boolean()
                    ->trueIcon('heroicon-o-lock-closed')
                    ->falseIcon('heroicon-o-lock-open')
                    ->trueColor('success')
                    ->falseColor('gray'),

                IconColumn::make('is_active')
                    ->label(__('Status'))
                    ->boolean()
                    ->trueIcon('heroicon-o-check-circle')
                    ->falseIcon('heroicon-o-x-circle')
                    ->trueColor('success')
                    ->falseColor('danger'),

                TextColumn::make('created_at')
                    ->label(__('Added'))
                    ->since()
                    ->dateTimeTooltip(),
            ])
            ->actions([
                Action::make('visit')
                    ->label(__('Visit'))
                    ->icon('heroicon-o-arrow-top-right-on-square')
                    ->url(fn (Domain $record): string => "https://{$record->domain}")
                    ->openUrlInNewTab()
                    ->size('sm'),
            ])
            ->headerActions([
                Action::make('manage')
                    ->label(__('Manage All'))
                    ->url(route('filament.jabali.pages.domains'))
                    ->button()
                    ->size('sm'),
            ])
            ->emptyStateHeading(__('No domains yet'))
            ->emptyStateDescription(__('Add your first domain to get started with your hosting.'))
            ->emptyStateIcon('heroicon-o-globe-alt')
            ->emptyStateActions([
                Action::make('add')
                    ->label(__('Add Domain'))
                    ->url(route('filament.jabali.pages.domains'))
                    ->icon('heroicon-o-plus')
                    ->button(),
            ])
            ->paginated(false);
    }

    protected function getTableQuery(): Builder
    {
        return Domain::query()
            ->where('user_id', Auth::id())
            ->with(['sslCertificate'])
            ->orderBy('created_at', 'desc')
            ->limit(5);
    }
}
