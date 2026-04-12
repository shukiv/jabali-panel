<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\NotificationChannels\Schemas;

use App\Models\NotificationChannel;
use Filament\Forms\Components\Repeater;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Schema;

/**
 * Dynamic form per channel type. Each row stores its config JSON in a
 * shape the matching driver knows how to read; we keep the Filament form
 * fields keyed under `config.*` so Eloquent's encrypted:array cast
 * serialises them straight to the DB.
 */
class NotificationChannelForm
{
    public static function configure(Schema $schema): Schema
    {
        return $schema
            ->components([
                TextInput::make('name')
                    ->required()
                    ->maxLength(100)
                    ->helperText('Short unique identifier — e.g. "ops_email", "critical_telegram".'),

                Select::make('type')
                    ->options([
                        'email' => 'Email',
                        'telegram' => 'Telegram',
                        'ntfy' => 'ntfy.sh (push)',
                        'web_push' => 'Web Push (browser)',
                    ])
                    ->required()
                    ->live()
                    ->disabledOn('edit')
                    ->helperText('Channel type cannot be changed after creation.'),

                // email
                Repeater::make('config.recipients')
                    ->label('Recipients')
                    ->simple(TextInput::make('email')->email()->required())
                    ->visible(fn (Get $get) => $get('type') === 'email')
                    ->minItems(1)
                    ->addActionLabel('Add recipient')
                    ->columnSpanFull(),

                // telegram
                TextInput::make('config.bot_token')
                    ->label('Bot Token')
                    ->required()
                    ->password()
                    ->revealable()
                    ->visible(fn (Get $get) => $get('type') === 'telegram')
                    ->helperText('Format: 1234567890:AA…  (obtain via @BotFather)'),
                TextInput::make('config.chat_id')
                    ->label('Chat ID')
                    ->required()
                    ->visible(fn (Get $get) => $get('type') === 'telegram')
                    ->helperText('Your numeric chat id or @channelusername. Send /start to the bot, then paste the id here.'),

                // ntfy
                TextInput::make('config.url')
                    ->label('Topic URL')
                    ->url()
                    ->required()
                    ->visible(fn (Get $get) => $get('type') === 'ntfy')
                    ->helperText('Full https URL to your ntfy topic, e.g. https://ntfy.sh/jabali-alerts-abc123'),
                TextInput::make('config.token')
                    ->label('Bearer Token (optional)')
                    ->password()
                    ->revealable()
                    ->visible(fn (Get $get) => $get('type') === 'ntfy')
                    ->helperText('Only needed for protected topics.'),

                // web_push
                TextInput::make('config.subject')
                    ->label('VAPID subject')
                    ->required()
                    ->default('mailto:admin@localhost')
                    ->visible(fn (Get $get) => $get('type') === 'web_push')
                    ->helperText('mailto: or https: URI identifying the app to browser push services.'),

                \Filament\Forms\Components\Toggle::make('enabled')
                    ->default(true)
                    ->inline(false),
            ]);
    }

    /**
     * Defaults injected on the CreateRecord page for each type so the form
     * feels live from first render.
     *
     * @return array<string, mixed>
     */
    public static function defaultsFor(string $type): array
    {
        return match ($type) {
            'email' => ['type' => 'email', 'enabled' => true, 'config' => ['recipients' => []]],
            'telegram' => ['type' => 'telegram', 'enabled' => true, 'config' => ['bot_token' => '', 'chat_id' => '']],
            'ntfy' => ['type' => 'ntfy', 'enabled' => true, 'config' => ['url' => '']],
            'web_push' => ['type' => 'web_push', 'enabled' => true, 'config' => ['subject' => 'mailto:admin@localhost']],
            default => [],
        };
    }

    /**
     * @return array<int, string>
     */
    public static function typeOptions(): array
    {
        return NotificationChannel::TYPES;
    }
}
