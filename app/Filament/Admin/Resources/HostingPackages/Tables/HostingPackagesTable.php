<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\HostingPackages\Tables;

use Filament\Actions\DeleteAction;
use Filament\Actions\EditAction;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Table;

class HostingPackagesTable
{
    public static function configure(Table $table): Table
    {
        return $table
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name'))
                    ->searchable()
                    ->sortable(),
                TextColumn::make('disk_quota_mb')
                    ->label(__('Disk'))
                    ->getStateUsing(function ($record) {
                        $quota = $record->disk_quota_mb;
                        if (! $quota) {
                            return __('Unlimited');
                        }

                        return $quota >= 1024
                            ? number_format($quota / 1024, 1).' GB'
                            : $quota.' MB';
                    }),
                TextColumn::make('bandwidth_gb')
                    ->label(__('Bandwidth'))
                    ->getStateUsing(fn ($record) => $record->bandwidth_gb ? $record->bandwidth_gb.' GB' : __('Unlimited')),
                TextColumn::make('domains_limit')
                    ->label(__('Domains'))
                    ->getStateUsing(fn ($record) => $record->domains_limit ?: __('Unlimited')),
                TextColumn::make('databases_limit')
                    ->label(__('Databases'))
                    ->getStateUsing(fn ($record) => $record->databases_limit ?: __('Unlimited')),
                TextColumn::make('mailboxes_limit')
                    ->label(__('Mailboxes'))
                    ->getStateUsing(fn ($record) => $record->mailboxes_limit ?: __('Unlimited')),
                IconColumn::make('is_active')
                    ->label(__('Active'))
                    ->boolean(),
            ])
            ->recordActions([
                EditAction::make(),
                DeleteAction::make(),
            ])
            ->defaultSort('name');
    }
}
