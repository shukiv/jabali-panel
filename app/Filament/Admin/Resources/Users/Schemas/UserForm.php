<?php

declare(strict_types=1);

namespace App\Filament\Admin\Resources\Users\Schemas;

use App\Models\DnsSetting;
use App\Models\HostingPackage;
use App\Support\PasswordGenerator;
use App\Support\WordList;
use Filament\Actions\Action;
use Filament\Forms\Components\DateTimePicker;
use Filament\Forms\Components\Placeholder;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Components\Utilities\Get;
use Filament\Schemas\Schema;
use Illuminate\Support\Facades\Hash;

class UserForm
{
    /**
     * Generate a secure password with uppercase, lowercase, numbers, and special characters
     */
    public static function generateSecurePassword(int $length = 16): string
    {
        return PasswordGenerator::generate($length);
    }

    public static function configure(Schema $schema): Schema
    {
        return $schema
            ->columns(1)
            ->components([
                Section::make(__('User Information'))
                    ->schema([
                        TextInput::make('name')
                            ->label(__('Name'))
                            ->required()
                            ->maxLength(255),

                        TextInput::make('username')
                            ->label(__('Username'))
                            ->required()
                            ->maxLength(32)
                            ->alphaNum()
                            ->unique(ignoreRecord: true)
                            ->rules(['regex:/^[a-z][a-z0-9_]{0,31}$/'])
                            ->helperText(__('Lowercase letters, numbers, and underscores only. Must start with a letter.'))
                            ->disabled(fn (string $operation): bool => $operation === 'edit'),

                        TextInput::make('email')
                            ->label(__('Email address'))
                            ->email()
                            ->required()
                            ->unique(ignoreRecord: true)
                            ->maxLength(255),

                        TextInput::make('password')
                            ->password()
                            ->revealable()
                            ->dehydrateStateUsing(fn ($state) => filled($state) ? Hash::make($state) : null)
                            ->dehydrated(fn ($state) => filled($state))
                            ->required(fn (string $operation): bool => $operation === 'create')
                            ->minLength(8)
                            ->rules(fn () => (bool) DnsSetting::get('passphrase_passwords') ? [] : [
                                'regex:/[a-z]/',
                                'regex:/[A-Z]/',
                                'regex:/[0-9]/',
                            ])
                            ->suffixActions([
                                Action::make('generatePassword')
                                    ->icon('heroicon-o-arrow-path')
                                    ->tooltip(__('Generate secure password'))
                                    ->action(function ($set) {
                                        $useWords = (bool) DnsSetting::get('passphrase_passwords');
                                        $password = $useWords ? WordList::generate() : self::generateSecurePassword();
                                        $set('password', $password);
                                    }),
                                Action::make('copyPassword')
                                    ->icon('heroicon-o-clipboard-document')
                                    ->tooltip(__('Copy to clipboard'))
                                    ->action(function ($state, $livewire) {
                                        if ($state) {
                                            $livewire->js('navigator.clipboard.writeText('.json_encode($state, JSON_HEX_TAG).')');
                                            \Filament\Notifications\Notification::make()
                                                ->title(__('Copied to clipboard'))
                                                ->success()
                                                ->duration(2000)
                                                ->send();
                                        }
                                    }),
                            ])
                            ->helperText(fn () => (bool) DnsSetting::get('passphrase_passwords')
                                ? __('Password will be generated as easy-to-remember words')
                                : __('Minimum 8 characters with uppercase, lowercase, and numbers'))
                            ->label(fn (string $operation): string => $operation === 'create' ? __('Password') : __('New Password')),
                    ])
                    ->columns(2),

                Section::make(__('Account Settings'))
                    ->schema([
                        Toggle::make('is_admin')
                            ->label(__('Administrator'))
                            ->helperText(__('Grant full administrative access'))
                            ->inline(false),

                        Toggle::make('is_active')
                            ->label(__('Active'))
                            ->default(true)
                            ->helperText(__('Inactive users cannot log in'))
                            ->inline(false),

                        Placeholder::make('package_notice')
                            ->label(__('Hosting Package'))
                            ->content(__('No hosting package selected. This user will have unlimited quotas.'))
                            ->visible(fn ($get) => blank($get('hosting_package_id'))),

                        Select::make('hosting_package_id')
                            ->label(__('Hosting Package'))
                            ->searchable()
                            ->preload()
                            ->options(fn () => ['' => __('No package (Unlimited)')] + HostingPackage::query()
                                ->where('is_active', true)
                                ->orderBy('name')
                                ->pluck('name', 'id')
                                ->toArray())
                            ->default('')
                            ->afterStateHydrated(fn ($state, $set) => $set('hosting_package_id', $state ?? ''))
                            ->dehydrateStateUsing(fn ($state) => filled($state) ? (int) $state : null)
                            ->helperText(__('Assign a package to set quotas.'))
                            ->columnSpanFull(),

                        Toggle::make('create_linux_user')
                            ->label(__('Create Linux User'))
                            ->default(true)
                            ->helperText(__('Create a system user account'))
                            ->visibleOn('create')
                            ->dehydrated(false)
                            ->inline(false),

                        Select::make('ssh_isolation_mode')
                            ->label(__('SSH Access Mode'))
                            ->columnSpan(2)
                            ->options([
                                'disabled' => __('Disabled — SFTP only'),
                                'container' => __('Container (nspawn) — high isolation'),
                                'sandbox' => __('Sandbox (bwrap) — moderate isolation, IDE-compatible'),
                                'standard' => __('Standard — plain shell, no isolation'),
                            ])
                            ->placeholder(__('Inherit from package'))
                            ->default(null)
                            ->helperText(fn (Get $get): string => match (true) {
                                blank($get('hosting_package_id')) => __('No package selected — defaults to Disabled'),
                                default => __('Leave empty to inherit from hosting package'),
                            }),

                        DateTimePicker::make('email_verified_at')
                            ->label(__('Email Verified At'))
                            ->columnSpan(2),
                    ])
                    ->columns(4),

                Section::make(__('System Information'))
                    ->schema([
                        Placeholder::make('home_directory_display')
                            ->label(__('Home Directory'))
                            ->content(fn ($record) => $record?->home_directory ?? '/home/'.__('username')),

                        Placeholder::make('created_at_display')
                            ->label(__('Created'))
                            ->content(fn ($record) => $record?->created_at?->format('M d, Y H:i') ?? __('N/A')),

                        Placeholder::make('updated_at_display')
                            ->label(__('Last Updated'))
                            ->content(fn ($record) => $record?->updated_at?->format('M d, Y H:i') ?? __('N/A')),
                    ])
                    ->columns(3)
                    ->visibleOn('edit')
                    ->collapsible(),
            ]);
    }
}
