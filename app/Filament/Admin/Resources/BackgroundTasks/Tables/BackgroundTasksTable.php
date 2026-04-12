<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\BackgroundTasks\Tables;

use App\Enums\BackgroundTaskStatus;
use App\Enums\BackgroundTaskType;
use App\Models\BackgroundTask;
use App\Services\Agent\AgentClientInterface;
use Filament\Actions\Action;
use Filament\Actions\ViewAction;
use Filament\Notifications\Notification as FilamentNotification;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Filters\SelectFilter;
use Filament\Tables\Table;

class BackgroundTasksTable
{
    public static function configure(Table $table): Table
    {
        return $table
            ->poll('5s')
            ->defaultSort('created_at', 'desc')
            ->columns([
                TextColumn::make('type')
                    ->label(__('Type'))
                    ->badge()
                    ->formatStateUsing(fn (BackgroundTaskType $state): string => $state->label())
                    ->searchable(),

                TextColumn::make('target_id')
                    ->label(__('Target'))
                    ->formatStateUsing(function (BackgroundTask $record): string {
                        if (! $record->target_type || ! $record->target_id) {
                            return '—';
                        }
                        $short = class_basename($record->target_type);

                        return "{$short}#{$record->target_id}";
                    })
                    ->toggleable(),

                TextColumn::make('status')
                    ->label(__('Status'))
                    ->badge()
                    ->formatStateUsing(fn (BackgroundTaskStatus $state): string => $state->label())
                    ->color(fn (BackgroundTaskStatus $state): string => $state->color()),

                TextColumn::make('progress')
                    ->label(__('Progress'))
                    ->formatStateUsing(function (BackgroundTask $record): string {
                        if ($record->status->isTerminal()) {
                            return $record->status === BackgroundTaskStatus::Done ? '100%' : '—';
                        }

                        return $record->progress !== null ? "{$record->progress}%" : '…';
                    }),

                TextColumn::make('step')
                    ->label(__('Step'))
                    ->limit(40)
                    ->toggleable(),

                TextColumn::make('started_at')
                    ->label(__('Started'))
                    ->since()
                    ->toggleable(),

                TextColumn::make('duration')
                    ->label(__('Duration'))
                    ->state(function (BackgroundTask $record): string {
                        $seconds = $record->durationSeconds();
                        if ($seconds === null) {
                            return '—';
                        }

                        return $seconds < 60 ? "{$seconds}s" : gmdate('H:i:s', $seconds);
                    })
                    ->toggleable(),
            ])
            ->filters([
                SelectFilter::make('status')
                    ->label(__('Status'))
                    ->options(collect(BackgroundTaskStatus::cases())
                        ->mapWithKeys(fn ($c) => [$c->value => $c->label()])
                        ->all()),

                SelectFilter::make('type')
                    ->label(__('Type'))
                    ->options(collect(BackgroundTaskType::cases())
                        ->mapWithKeys(fn ($c) => [$c->value => $c->label()])
                        ->all()),
            ])
            ->recordActions([
                ViewAction::make()
                    ->modal()
                    ->modalContent(fn (BackgroundTask $record) => view(
                        'filament.admin.resources.background-tasks.detail',
                        ['task' => $record->fresh()]
                    )),

                Action::make('cancel')
                    ->label(__('Cancel'))
                    ->icon('heroicon-o-x-circle')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->visible(fn (BackgroundTask $record): bool => ! $record->status->isTerminal())
                    ->action(function (BackgroundTask $record) {
                        $agent = app(AgentClientInterface::class);
                        try {
                            $response = $agent->send('job.cancel', ['id' => $record->id]);
                            if (! empty($response['success'])) {
                                FilamentNotification::make()
                                    ->title(__('Cancellation requested'))
                                    ->success()->send();
                            } else {
                                FilamentNotification::make()
                                    ->title(__('Cancel failed'))
                                    ->body((string) ($response['error'] ?? 'unknown error'))
                                    ->danger()->send();
                            }
                        } catch (\Throwable $e) {
                            FilamentNotification::make()
                                ->title(__('Cancel failed'))
                                ->body($e->getMessage())
                                ->danger()->send();
                        }
                    }),
            ]);
    }
}
