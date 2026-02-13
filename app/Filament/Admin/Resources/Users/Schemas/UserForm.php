<?php

namespace App\Filament\Admin\Resources\Users\Schemas;

use App\Models\HostingPackage;
use Filament\Actions\Action;
use Filament\Forms\Components\DateTimePicker;
use Filament\Forms\Components\Placeholder;
use Filament\Forms\Components\Select;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Components\Toggle;
use Filament\Schemas\Components\Section;
use Filament\Schemas\Schema;
use Illuminate\Support\Facades\Hash;

class UserForm
{
    /**
     * Generate a secure password with uppercase, lowercase, numbers, and special characters
     */
    public static function generateSecurePassword(int $length = 16): string
    {
        $uppercase = 'ABCDEFGHJKLMNPQRSTUVWXYZ';
        $lowercase = 'abcdefghjkmnpqrstuvwxyz';
        $numbers = '23456789';
        $special = '!@#$%^&*';

        // Ensure at least one of each type
        $password = $uppercase[random_int(0, strlen($uppercase) - 1)]
            .$lowercase[random_int(0, strlen($lowercase) - 1)]
            .$numbers[random_int(0, strlen($numbers) - 1)]
            .$special[random_int(0, strlen($special) - 1)];

        // Fill rest with random characters from all pools
        $allChars = $uppercase.$lowercase.$numbers.$special;
        for ($i = 4; $i < $length; $i++) {
            $password .= $allChars[random_int(0, strlen($allChars) - 1)];
        }

        // Shuffle the password
        return str_shuffle($password);
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
                            ->rules([
                                'regex:/[a-z]/',      // lowercase
                                'regex:/[A-Z]/',      // uppercase
                                'regex:/[0-9]/',      // number
                            ])
                            ->suffixActions([
                                Action::make('generatePassword')
                                    ->icon('heroicon-o-arrow-path')
                                    ->tooltip(__('Generate secure password'))
                                    ->action(function ($set) {
                                        $password = self::generateSecurePassword();
                                        $set('password', $password);
                                    }),
                                Action::make('copyPassword')
                                    ->icon('heroicon-o-clipboard-document')
                                    ->tooltip(__('Copy to clipboard'))
                                    ->action(function ($state, $livewire) {
                                        if ($state) {
                                            $escaped = addslashes($state);
                                            $livewire->js("navigator.clipboard.writeText('{$escaped}')");
                                            \Filament\Notifications\Notification::make()
                                                ->title(__('Copied to clipboard'))
                                                ->success()
                                                ->duration(2000)
                                                ->send();
                                        }
                                    }),
                            ])
                            ->helperText(__('Minimum 8 characters with uppercase, lowercase, and numbers'))
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

                        DateTimePicker::make('email_verified_at')
                            ->label(__('Email Verified At')),
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
