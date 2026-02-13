<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages;

use App\Models\UserSetting;
use BackedEnum;
use Filament\Actions\Concerns\InteractsWithActions;
use Filament\Actions\Contracts\HasActions;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\Page;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;

class MailingLists extends Page implements HasActions, HasForms
{
    use InteractsWithActions;
    use InteractsWithForms;

    protected static string|BackedEnum|null $navigationIcon = 'heroicon-o-paper-airplane';

    protected static ?int $navigationSort = 22;

    protected static ?string $slug = 'mailing-lists';

    protected string $view = 'filament.jabali.pages.mailing-lists';

    public array $mailingFormData = [];

    public function getTitle(): string|Htmlable
    {
        return __('Mailing Lists');
    }

    public static function getNavigationLabel(): string
    {
        return __('Mailing Lists');
    }

    public function mount(): void
    {
        $this->mailingFormData = UserSetting::getForUser(Auth::id(), 'mailing_lists', [
            'provider' => 'none',
            'listmonk_url' => '',
            'listmonk_token' => '',
            'listmonk_list_id' => '',
            'mailman_url' => '',
            'mailman_admin' => '',
        ]);
    }

    protected function getForms(): array
    {
        return ['mailingForm'];
    }

    public function mailingForm(Schema $schema): Schema
    {
        return $schema
            ->statePath('mailingFormData')
            ->schema([
                Section::make(__('Provider'))
                    ->schema([
                        Select::make('provider')
                            ->label(__('Mailing List Provider'))
                            ->options([
                                'none' => __('None'),
                                'listmonk' => __('Listmonk'),
                                'mailman' => __('Mailman'),
                            ])
                            ->default('none')
                            ->live(),
                    ]),
                Section::make(__('Listmonk'))
                    ->schema([
                        TextInput::make('listmonk_url')
                            ->label(__('Listmonk URL'))
                            ->placeholder(__('https://lists.example.com'))
                            ->url()
                            ->visible(fn ($get) => $get('provider') === 'listmonk'),
                        TextInput::make('listmonk_token')
                            ->label(__('API Token'))
                            ->password()
                            ->revealable()
                            ->visible(fn ($get) => $get('provider') === 'listmonk'),
                        TextInput::make('listmonk_list_id')
                            ->label(__('Default List ID'))
                            ->visible(fn ($get) => $get('provider') === 'listmonk'),
                    ])
                    ->columns(2),
                Section::make(__('Mailman'))
                    ->schema([
                        TextInput::make('mailman_url')
                            ->label(__('Mailman URL'))
                            ->placeholder(__('https://lists.example.com/mailman'))
                            ->url()
                            ->visible(fn ($get) => $get('provider') === 'mailman'),
                        TextInput::make('mailman_admin')
                            ->label(__('Mailman Admin Email'))
                            ->email()
                            ->visible(fn ($get) => $get('provider') === 'mailman'),
                    ])
                    ->columns(2),
            ]);
    }

    public function saveSettings(): void
    {
        UserSetting::setForUser(Auth::id(), 'mailing_lists', $this->mailingForm->getState());

        Notification::make()
            ->title(__('Mailing list settings saved'))
            ->success()
            ->send();
    }
}
