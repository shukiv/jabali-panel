<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Widgets;

use App\JabaliSecurity\JabaliSecurityClient;
use App\JabaliSecurity\Pages\Security;
use Filament\Actions\Action;
use Filament\Notifications\Notification;
use Filament\Tables\Columns\IconColumn;
use Filament\Tables\Columns\TextColumn;
use Filament\Tables\Concerns\InteractsWithTable;
use Filament\Tables\Contracts\HasTable;
use Filament\Tables\Table;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Schemas\Concerns\InteractsWithSchemas;
use Filament\Schemas\Contracts\HasSchemas;
use Livewire\Component;

class WebshieldRulesTable extends Component implements HasActions, HasSchemas, HasTable
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
            ->records(fn () => $this->client()->get('/webshield/rules') ?? [])
            ->columns([
                TextColumn::make('name')
                    ->label(__('Name')),
                TextColumn::make('pattern')
                    ->label(__('Pattern'))
                    ->limit(40),
                TextColumn::make('action')
                    ->label(__('Action'))
                    ->badge()
                    ->color(fn (string $state): string => match ($state) {
                        'block' => 'danger',
                        'challenge' => 'warning',
                        'allow' => 'success',
                        default => 'gray',
                    }),
                TextColumn::make('category')
                    ->label(__('Category'))
                    ->badge()
                    ->color('gray'),
                IconColumn::make('enabled')
                    ->label(__('Enabled'))
                    ->boolean(),
            ])
            ->headerActions([
                Action::make('enable')
                    ->label(__('Enable'))
                    ->icon('heroicon-o-arrow-down-on-square')
                    ->requiresConfirmation()
                    ->action(function (): void {
                        $this->client()->patch('/config', ['WEBSHIELD_ENABLED' => 'yes']);
                        $result = $this->client()->post('/webshield/install');

                        Notification::make()
                            ->title($result ? __('WebShield enabled') : __('Failed to enable WebShield'))
                            ->{($result ? "success" : "danger")}()
                            ->send();
                        $this->redirect(Security::tabUrl('defense', 'webshield'), navigate: true);
                    }),
                Action::make('disable')
                    ->label(__('Disable'))
                    ->icon('heroicon-o-x-circle')
                    ->color('danger')
                    ->requiresConfirmation()
                    ->action(function (): void {
                        $result = $this->client()->post('/webshield/uninstall');
                        $this->client()->patch('/config', ['WEBSHIELD_ENABLED' => 'no']);

                        Notification::make()
                            ->title($result ? __('WebShield disabled') : __('Failed to disable WebShield'))
                            ->{($result ? "success" : "danger")}()
                            ->send();
                    }),
            ])
            ->emptyStateHeading(__('No WebShield rules'))
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
