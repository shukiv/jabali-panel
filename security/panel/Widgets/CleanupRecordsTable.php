<?php

declare(strict_types=1);

namespace App\JabaliSecurity\Widgets;

use App\JabaliSecurity\JabaliSecurityClient;
use Filament\Actions\Action;
use Filament\Forms\Components\TextInput;
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

class CleanupRecordsTable extends Component implements HasActions, HasSchemas, HasTable
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
            ->records(fn () => $this->client()->get('/cleanup/records') ?? [])
            ->columns([
                TextColumn::make('path')
                    ->label(__('Path'))
                    ->limit(40),
                TextColumn::make('strategy')
                    ->label(__('Strategy'))
                    ->badge()
                    ->color('gray'),
                IconColumn::make('success')
                    ->label(__('Success'))
                    ->boolean(),
                TextColumn::make('username')
                    ->label(__('Username')),
                TextColumn::make('created_at')
                    ->label(__('Time'))
                    ->since(),
            ])
            ->headerActions([
                Action::make('clean_file')
                    ->label(__('Clean File'))
                    ->icon('heroicon-o-trash')
                    ->requiresConfirmation()
                    ->form([
                        TextInput::make('path')
                            ->label(__('File Path'))
                            ->required()
                            ->rules(['regex:/^(\/home\/[^\/]+\/|\/var\/www\/).*$/'])
                            ->validationMessages(['regex' => __('Path must be under /home/ or /var/www/')]),
                    ])
                    ->action(function (array $data): void {
                        $path = $data['path'] ?? '';
                        if (str_contains($path, '..') || ! preg_match('#^(/home/[^/]+/|/var/www/)#', $path)) {
                            Notification::make()->title(__('Invalid path'))->danger()->send();
                            return;
                        }
                        $result = $this->client()->post('/cleanup/file', $data);

                        Notification::make()
                            ->title($result ? __('File cleaned') : __('Failed to clean file'))
                            ->{($result ? "success" : "danger")}()
                            ->send();
                    }),
            ])
            ->emptyStateHeading(__('No cleanup records'))
            ->striped();
    }

    public function render()
    {
        return $this->getTable()->render();
    }
}
