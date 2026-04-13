<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Widgets;

use App\JabaliSecurity\JabaliSecurityClient;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Livewire\Component;

class UsersTable extends Component implements HasActions, HasSchemas, HasTable
{
    use InteractsWithActions;
    use InteractsWithSchemas;
    use InteractsWithTable;

    protected function client(): JabaliSecurityClient
    {
        return JabaliSecurityClient::getInstance();
    }

    public function table(Table $table): Table
    {
        return $table
            ->records(fn () => $this->client()->get('/users') ?? [])
            ->columns([
                TextColumn::make('username')
                    ->label(__('Username')),
                TextColumn::make('incident_count')
                    ->label(__('Incidents')),
                TextColumn::make('max_score')
                    ->label(__('Max Score'))
                    ->color(fn ($state): string => match (true) {
                        (int) $state >= 70 => 'danger',
                        (int) $state >= 40 => 'warning',
                        default => 'success',
                    }),
            ])
            ->emptyStateHeading(__('No users'))
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
