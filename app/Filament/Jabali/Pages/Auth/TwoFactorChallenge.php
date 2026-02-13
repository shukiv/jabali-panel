<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages\Auth;

use Filament\Facades\Filament;
use Filament\Forms\Components\TextInput;
use Filament\Forms\Concerns\InteractsWithForms;
use Filament\Forms\Contracts\HasForms;
use Filament\Notifications\Notification;
use Filament\Pages\SimplePage;
use Filament\Schemas\Schema;
use Illuminate\Contracts\Support\Htmlable;
use Illuminate\Support\Facades\Auth;
use Laravel\Fortify\Contracts\TwoFactorAuthenticationProvider;
use Laravel\Fortify\Events\RecoveryCodeReplaced;

class TwoFactorChallenge extends SimplePage implements HasForms
{
    use InteractsWithForms;

    protected string $view = 'filament.jabali.pages.auth.two-factor-challenge';

    public ?array $data = [];

    public bool $useRecoveryCode = false;

    public function mount(): void
    {
        // If not in 2FA challenge state, redirect to login
        if (! session('login.id')) {
            $this->redirect(Filament::getLoginUrl());

            return;
        }

        $this->form->fill();
    }

    public function getTitle(): string|Htmlable
    {
        return __('Two-Factor Authentication');
    }

    public function getHeading(): string|Htmlable
    {
        return __('Two-Factor Authentication');
    }

    public function getSubheading(): string|Htmlable|null
    {
        return $this->useRecoveryCode
            ? __('Please enter one of your emergency recovery codes.')
            : __('Please enter the authentication code from your app.');
    }

    public function form(Schema $schema): Schema
    {
        return $schema
            ->schema([
                TextInput::make('code')
                    ->label($this->useRecoveryCode ? __('Recovery Code') : __('Authentication Code'))
                    ->placeholder($this->useRecoveryCode ? __('Enter recovery code') : '000000')
                    ->required()
                    ->autocomplete('one-time-code')
                    ->autofocus()
                    ->extraInputAttributes([
                        'inputmode' => $this->useRecoveryCode ? 'text' : 'numeric',
                        'pattern' => $this->useRecoveryCode ? null : '[0-9]*',
                        'maxlength' => $this->useRecoveryCode ? 21 : 6,
                    ]),
            ])
            ->statePath('data');
    }

    public function authenticate(): void
    {
        $data = $this->form->getState();
        $code = $data['code'];

        $user = $this->getChallengedUser();

        if (! $user) {
            $this->redirect(Filament::getLoginUrl());

            return;
        }

        $valid = $this->useRecoveryCode
            ? $this->validateRecoveryCode($user, $code)
            : $this->validateAuthenticationCode($user, $code);

        if (! $valid) {
            Notification::make()
                ->title(__('Invalid Code'))
                ->body($this->useRecoveryCode
                    ? __('The recovery code is invalid.')
                    : __('The authentication code is invalid.'))
                ->danger()
                ->send();

            $this->form->fill();

            return;
        }

        // Clear the challenge session
        session()->forget('login.id');
        session()->forget('login.remember');

        // Login the user
        Auth::guard(Filament::getAuthGuard())->login($user, session('login.remember', false));

        session()->regenerate();

        $this->redirect(Filament::getUrl());
    }

    protected function getChallengedUser()
    {
        $userId = session('login.id');

        if (! $userId) {
            return null;
        }

        return \App\Models\User::find($userId);
    }

    protected function validateAuthenticationCode($user, string $code): bool
    {
        return app(TwoFactorAuthenticationProvider::class)->verify(
            decrypt($user->two_factor_secret),
            $code
        );
    }

    protected function validateRecoveryCode($user, string $code): bool
    {
        $codes = json_decode(decrypt($user->two_factor_recovery_codes), true);

        $code = str_replace('-', '', trim($code));

        foreach ($codes as $index => $storedCode) {
            $storedCode = str_replace('-', '', $storedCode);
            if (hash_equals($storedCode, $code)) {
                // Remove the used code
                unset($codes[$index]);
                $user->forceFill([
                    'two_factor_recovery_codes' => encrypt(json_encode(array_values($codes))),
                ])->save();

                event(new RecoveryCodeReplaced($user, $code));

                return true;
            }
        }

        return false;
    }

    public function toggleRecoveryCode(): void
    {
        $this->useRecoveryCode = ! $this->useRecoveryCode;
        $this->form->fill();
    }

    protected function hasFullWidthFormActions(): bool
    {
        return true;
    }
}
