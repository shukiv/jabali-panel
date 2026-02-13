<?php

declare(strict_types=1);

namespace App\Filament\Admin\Widgets\Dashboard;

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

class RecentActivityTable extends Component implements HasTable, HasSchemas, HasActions
{
    use InteractsWithTable;
    use InteractsWithSchemas;
    use InteractsWithActions;

    public function makeFilamentTranslatableContentDriver(): ?\Filament\Support\Contracts\TranslatableContentDriver
    {
        return null;
    }

    public function table(Table $table): Table
    {
        return $table
            ->query(AuditLog::query()->with('user')->latest()->limit(10))
            ->columns([
                TextColumn::make('created_at')
                    ->label(__('Time'))
                    ->dateTime('M d, H:i')
                    ->color('gray'),
                TextColumn::make('user.name')
                    ->label(__('User'))
                    ->icon('heroicon-o-user')
                    ->default(__('System')),
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
                    ->limit(50)
                    ->wrap(),
            ])
            ->paginated(false)
            ->striped()
            ->emptyStateHeading(__('No activity'))
            ->emptyStateDescription(__('No recent activity recorded.'))
            ->emptyStateIcon('heroicon-o-document-text');
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
