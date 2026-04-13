<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Widgets;

use App\JabaliSecurity\JabaliSecurityClient;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Livewire\Component;

class WafEventsTable extends Component implements HasActions, HasSchemas, HasTable
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
            ->records(fn () => $this->client()->get('/waf/events', ['limit' => 100]) ?? [])
            ->columns([
                TextColumn::make('client_ip')
                    ->label(__('Client IP'))
                    ->copyable(),
                TextColumn::make('method')
                    ->label(__('Method'))
                    ->badge()
                    ->color('gray'),
                TextColumn::make('uri')
                    ->label(__('URI'))
                    ->limit(40),
                TextColumn::make('rule_id')
                    ->label(__('Rule ID')),
                TextColumn::make('rule_msg')
                    ->label(__('Message'))
                    ->limit(30),
                TextColumn::make('severity')
                    ->label(__('Severity'))
                    ->badge(),
                TextColumn::make('created_at')
                    ->label(__('Time'))
                    ->since(),
            ])
            ->recordActions([
                Action::make('disable_rule')
                    ->label(__('Disable Rule'))
                    ->icon('heroicon-o-x-circle')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(function (array $record): void {
                        $result = $this->client()->post("/waf/rules/{$record['rule_id']}/disable");

                        Notification::make()
                            ->title($result ? __('Rule disabled') : __('Failed to disable rule'))
                            ->{($result ? "success" : "danger")}()
                            ->send();
                    }),
            ])
            ->emptyStateHeading(__('No WAF events'))
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
