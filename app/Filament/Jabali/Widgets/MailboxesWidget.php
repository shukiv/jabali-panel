<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

use App\Models\Mailbox;
use Filament\Actions\Action;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Table;
use Filament\Widgets\TableWidget as BaseWidget;
use Illuminate\Database\Eloquent\Builder;
use Illuminate\Support\Facades\Auth;

class MailboxesWidget extends BaseWidget
{
    protected static ?int $sort = 3;

    protected int|string|array $columnSpan = 1;

    public function table(Table $table): Table
    {
        return $table
            ->query($this->getTableQuery())
            ->heading(__('Your Mailboxes'))
            ->description(__('Recent mailboxes in your account'))
            ->columns([
                TextColumn::make('email')
                    ->label(__('Email'))
                    ->icon('heroicon-o-envelope')
                    ->weight('medium'),

                IconColumn::make('is_active')
                    ->label(__('Status'))
                    ->boolean()
                    ->trueIcon('heroicon-o-check-circle')
                    ->falseIcon('heroicon-o-x-circle')
                    ->trueColor('success')
                    ->falseColor('danger'),

                TextColumn::make('quota_formatted')
                    ->label(__('Quota')),

                TextColumn::make('created_at')
                    ->label(__('Added'))
                    ->since()
                    ->dateTimeTooltip(),
            ])
            ->actions([
                Action::make('webmail')
                    ->label(__('Webmail'))
                    ->icon('heroicon-o-arrow-top-right-on-square')
                    ->url(fn (): string => '/webmail/')
                    ->openUrlInNewTab()
                    ->size('sm'),
            ])
            ->headerActions([
                Action::make('manage')
                    ->label(__('Manage All'))
                    ->url(route('filament.jabali.pages.email'))
                    ->button()
                    ->size('sm'),
            ])
            ->emptyStateHeading(__('No mailboxes yet'))
            ->emptyStateDescription(__('Create your first mailbox to start receiving emails.'))
            ->emptyStateIcon('heroicon-o-envelope')
            ->emptyStateActions([
                Action::make('add')
                    ->label(__('Add Mailbox'))
                    ->url(route('filament.jabali.pages.email'))
                    ->icon('heroicon-o-plus')
                    ->button(),
            ])
            ->paginated(false);
    }

    protected function getTableQuery(): Builder
    {
        return Mailbox::query()
            ->where('user_id', Auth::id())
            ->with(['emailDomain'])
            ->orderBy('created_at', 'desc')
            ->limit(5);
    }
}
