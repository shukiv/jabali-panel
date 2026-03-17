<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\GeoBlockRules\Tables;

use App\Services\System\GeoBlockService;
use App\Support\SafeError;
use Filament\Actions\DeleteAction;
use Filament\Actions\EditAction;
use Filament\Notifications\Notification;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Table;

class GeoBlockRulesTable
{
    public static function configure(Table $table): Table
    {
        return $table
            ->columns([
                TextColumn::make('country_code')
                    ->label(__('Country'))
                    ->formatStateUsing(fn ($state) => strtoupper($state))
                    ->searchable(),
                TextColumn::make('action')
                    ->label(__('Action'))
                    ->badge()
                    ->color(fn (string $state): string => $state === 'block' ? 'danger' : 'success'),
                TextColumn::make('notes')
                    ->label(__('Notes'))
                    ->wrap(),
                IconColumn::make('is_active')
                    ->label(__('Active'))
                    ->boolean(),
            ])
            ->recordActions([
                EditAction::make(),
                DeleteAction::make()
                    ->after(function () {
                        try {
                            app(GeoBlockService::class)->applyCurrentRules();
                        } catch (\Exception $e) {
                            Notification::make()
                                ->title(__('Geo rules sync failed'))
                                ->body(SafeError::message($e))
                                ->danger()
                                ->send();
                        }
                    }),
            ])
            ->emptyStateHeading(__('No geo rules'))
            ->emptyStateDescription(__('Add a country rule to allow or block traffic.'));
    }
}
