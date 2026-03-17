<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Widgets;

use App\Models\AuditLog;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Illuminate\Support\Facades\Auth;
use Livewire\Component;

class ActivityLogTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    public function table(Table $table): Table
    {
        return $table
            ->query(
                AuditLog::query()
                    ->where('user_id', Auth::id())
                    ->latest()
            )
            ->columns([
                TextColumn::make('created_at')
                    ->label(__('Time'))
                    ->dateTime('M d, H:i')
                    ->color('gray'),
                TextColumn::make('category')
                    ->label(__('Category'))
                    ->badge()
                    ->color(fn (string $state): string => match ($state) {
                        'domain' => 'info',
                        'email' => 'primary',
                        'database' => 'warning',
                        'auth' => 'gray',
                        'firewall' => 'danger',
                        'service' => 'success',
                        default => 'gray',
                    }),
                TextColumn::make('action')
                    ->label(__('Action'))
                    ->badge()
                    ->color(fn (string $state): string => match ($state) {
                        'create', 'created' => 'success',
                        'update', 'updated' => 'warning',
                        'delete', 'deleted' => 'danger',
                        'login' => 'info',
                        default => 'gray',
                    }),
                TextColumn::make('description')
                    ->label(__('Description'))
                    ->limit(60)
                    ->wrap(),
                TextColumn::make('ip_address')
                    ->label(__('IP'))
                    ->color('gray'),
            ])
            ->defaultPaginationPageOption(25)
            ->striped()
            ->emptyStateHeading(__('No activity recorded yet'))
            ->emptyStateDescription(__('Recent actions performed in your account will appear here.'))
            ->emptyStateIcon('heroicon-o-clipboard-document-list');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
