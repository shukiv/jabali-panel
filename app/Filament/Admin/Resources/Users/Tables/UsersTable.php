<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\Users\Tables;

use App\Services\System\LinuxUserService;
use Exception;
use Filament\Actions\Action;
use Filament\Actions\DeleteAction;
use Filament\Actions\DeleteBulkAction;
use Filament\Actions\EditAction;
use Filament\Forms\Components\Toggle;
use Filament\Notifications\Notification;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Filters\TernaryFilter;
use Filament\Tables\Table;

class UsersTable
{
    public static function configure(Table $table): Table
    {
        return $table
            ->columns([
                TextColumn::make('id')
                    ->label(__('ID'))
                    ->sortable(),

                TextColumn::make('name')
                    ->label(__('Name'))
                    ->searchable()
                    ->sortable(),

                TextColumn::make('username')
                    ->label(__('Username'))
                    ->searchable()
                    ->sortable()
                    ->copyable(),

                TextColumn::make('email')
                    ->label(__('Email'))
                    ->searchable()
                    ->sortable(),

                TextColumn::make('home_directory')
                    ->label(__('Home'))
                    ->toggleable(isToggledHiddenByDefault: true),

                IconColumn::make('is_admin')
                    ->boolean()
                    ->label(__('Admin')),

                IconColumn::make('is_active')
                    ->boolean()
                    ->label(__('Active')),

                TextColumn::make('disk_usage')
                    ->label(__('Disk Usage'))
                    ->getStateUsing(function ($record) {
                        $used = $record->disk_usage_formatted;
                        $quotaMb = $record->disk_quota_mb;

                        if (! $quotaMb || $quotaMb <= 0) {
                            return $used;
                        }

                        $quota = $quotaMb >= 1024
                            ? number_format($quotaMb / 1024, 1).' GB'
                            : $quotaMb.' MB';

                        return "{$used} / {$quota}";
                    })
                    ->description(function ($record) {
                        if (! $record->disk_quota_mb || $record->disk_quota_mb <= 0) {
                            return __('Unlimited');
                        }

                        return $record->disk_usage_percent.'% '.__('used');
                    })
                    ->color(function ($record) {
                        if (! $record->disk_quota_mb || $record->disk_quota_mb <= 0) {
                            return 'gray';
                        }
                        $percent = $record->disk_usage_percent;
                        if ($percent >= 90) {
                            return 'danger';
                        }
                        if ($percent >= 75) {
                            return 'warning';
                        }

                        return null;
                    }),

                TextColumn::make('created_at')
                    ->label(__('Created'))
                    ->dateTime()
                    ->sortable()
                    ->toggleable(isToggledHiddenByDefault: true),
            ])
            ->filters([
                TernaryFilter::make('is_admin')
                    ->label(__('Administrator')),

                TernaryFilter::make('is_active')
                    ->label(__('Active')),
            ])
            ->recordActions([
                Action::make('loginAsUser')
                    ->label(__('Login as User'))
                    ->icon('heroicon-o-arrow-right-on-rectangle')
                    ->color('info')
                    ->visible(fn ($record) => ! $record->is_admin && $record->is_active)
                    ->url(fn ($record) => route('impersonate.start', ['user' => $record->id]), shouldOpenInNewTab: true),
                EditAction::make(),
                DeleteAction::make()
                    ->visible(fn ($record) => (int) $record->id !== 1)
                    ->form([
                        Toggle::make('remove_home')
                            ->label(__('Delete home directory'))
                            ->helperText(fn ($record) => __('Warning: This will permanently delete /home/:username and all its contents!', ['username' => $record->username]))
                            ->default(false),
                    ])
                    ->action(function ($record, array $data) {
                        $removeHome = $data['remove_home'] ?? false;
                        $username = $record->username;
                        $steps = [];

                        try {
                            $linuxService = new LinuxUserService;
                            $domains = $record->domains()->pluck('domain')->all();

                            if ($linuxService->userExists($username)) {
                                $result = $linuxService->deleteUser($username, $removeHome, $domains);

                                if (! ($result['success'] ?? false)) {
                                    throw new Exception($result['error'] ?? __('Failed to delete Linux user'));
                                }

                                $steps = array_merge($steps, $result['steps'] ?? []);
                            } else {
                                $steps[] = __('Linux user not found on the server');
                            }
                        } catch (Exception $e) {
                            Notification::make()
                                ->title(__('Linux user deletion failed'))
                                ->body($e->getMessage())
                                ->danger()
                                ->send();
                        }

                        $record->delete();

                        $steps[] = __('Removed user from admin list');
                        $details = implode("\n", array_map(fn ($step): string => '• '.$step, $steps));

                        Notification::make()
                            ->title(__('User :username removed', ['username' => $username]))
                            ->body($details)
                            ->success()
                            ->send();
                    }),
            ])
            ->bulkActions([
                DeleteBulkAction::make()
                    ->form([
                        Toggle::make('remove_home')
                            ->label(__('Delete home directories'))
                            ->helperText(__('Warning: This will permanently delete all home directories for selected users!'))
                            ->default(false),
                    ])
                    ->action(function ($records, array $data) {
                        $removeHome = $data['remove_home'] ?? false;
                        $linuxService = new LinuxUserService;
                        $skippedPrimaryAdmin = 0;
                        $deletedCount = 0;

                        foreach ($records as $record) {
                            if ((int) $record->id === 1) {
                                $skippedPrimaryAdmin++;

                                continue;
                            }

                            try {
                                if ($linuxService->userExists($record->username)) {
                                    $linuxService->deleteUser($record->username, $removeHome);
                                }
                            } catch (Exception $e) {
                                Notification::make()
                                    ->title(__('Failed to delete Linux user :username', ['username' => $record->username]))
                                    ->body($e->getMessage())
                                    ->danger()
                                    ->send();
                            }

                            $record->delete();
                            $deletedCount++;
                        }

                        if ($skippedPrimaryAdmin > 0) {
                            Notification::make()
                                ->title(__('Primary admin account cannot be deleted'))
                                ->body(__(':count account(s) were skipped.', ['count' => $skippedPrimaryAdmin]))
                                ->warning()
                                ->send();
                        }

                        if ($deletedCount > 0) {
                            Notification::make()
                                ->title(__('Users deleted'))
                                ->success()
                                ->send();
                        } else {
                            Notification::make()
                                ->title(__('No users deleted'))
                                ->warning()
                                ->send();
                        }
                    }),
            ])
            ->checkIfRecordIsSelectableUsing(fn ($record): bool => (int) $record->id !== 1);
    }
}
