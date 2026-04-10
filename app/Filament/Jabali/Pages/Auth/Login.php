<?php

declare(strict_types=1);

namespace App\Filament\Jabali\Pages\Auth;

use DanHarrin\LivewireRateLimiting\Exceptions\TooManyRequestsException;
use Filament\Auth\Http\Responses\Contracts\LoginResponse;
use Filament\Auth\MultiFactor\Contracts\HasBeforeChallengeHook;
use Filament\Auth\Pages\Login as BaseLogin;
use Filament\Facades\Filament;
use Filament\Forms\Components\TextInput;
use Filament\Models\Contracts\FilamentUser;
use Filament\Schemas\Components\Component;
use Illuminate\Contracts\Auth\Guard;
use Illuminate\Support\Facades\Auth;
use Illuminate\Support\Facades\Cache;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\HtmlString;
use SensitiveParameter;

class Login extends BaseLogin
{
    public function getSubheading(): string|HtmlString|null
    {
        if (config('app.demo_mode')) {
            return new HtmlString(__('Demo mode — explore the panel freely'));
        }

        return parent::getSubheading();
    }

    public function authenticate(): ?LoginResponse
    {
        $panel = Filament::getPanel('jabali');
        Filament::setCurrentPanel($panel);

        try {
            $this->rateLimit(5);
        } catch (TooManyRequestsException $exception) {
            $this->getRateLimitedNotification($exception)?->send();

            return null;
        }

        $data = $this->form->getState();

        $login = mb_strtolower($data['email']);
        $lockoutKey = 'login_lockout:'.sha1($login);
        $attemptsKey = 'login_attempts:'.sha1($login);

        // Check if account is locked out
        if (Cache::has($lockoutKey)) {
            $this->throwFailureValidationException();
        }

        /** @var Guard $authGuard */
        $authGuard = Auth::guard($panel->getAuthGuard());
        $authProvider = $authGuard->getProvider();
        $credentials = $this->getCredentialsFromFormData($data);

        $user = $authProvider->retrieveByCredentials($credentials);

        if (! $user) {
            // Constant-time: prevent user enumeration via timing
            Hash::check($credentials['password'], '$2y$12$VG9EWpdymMd.phHpJLWwauaoYk0mufInpXAzNUqZcMXm/WszkDs42');
        }

        if ((! $user) || (! $authProvider->validateCredentials($user, $credentials))) {
            $this->userUndertakingMultiFactorAuthentication = null;

            // Track failed attempts and lock after 10
            Cache::add($attemptsKey, 0, now()->addMinutes(15));
            $attempts = Cache::increment($attemptsKey);

            if ($attempts >= 10) {
                Cache::put($lockoutKey, true, now()->addMinutes(15));
                Cache::forget($attemptsKey);
            }

            $this->fireFailedEvent($authGuard, $user, $credentials);
            $this->throwFailureValidationException();
        }

        // Clear failed attempts on successful login
        Cache::forget($attemptsKey);
        Cache::forget($lockoutKey);

        if (
            filled($this->userUndertakingMultiFactorAuthentication) &&
            (decrypt($this->userUndertakingMultiFactorAuthentication) === $user->getAuthIdentifier())
        ) {
            $this->multiFactorChallengeForm->validate();
        } else {
            foreach (Filament::getMultiFactorAuthenticationProviders() as $multiFactorAuthenticationProvider) {
                if (! $multiFactorAuthenticationProvider->isEnabled($user)) {
                    continue;
                }

                $this->userUndertakingMultiFactorAuthentication = encrypt($user->getAuthIdentifier());

                if ($multiFactorAuthenticationProvider instanceof HasBeforeChallengeHook) {
                    $multiFactorAuthenticationProvider->beforeChallenge($user);
                }

                break;
            }

            if (filled($this->userUndertakingMultiFactorAuthentication)) {
                $this->multiFactorChallengeForm->fill();

                return null;
            }
        }

        if ($user instanceof FilamentUser && ! $user->canAccessPanel($panel)) {
            $this->fireFailedEvent($authGuard, $user, $credentials);
            $this->throwFailureValidationException();
        }

        $authGuard->login($user, $data['remember'] ?? false);

        session()->regenerate();

        return app(LoginResponse::class);
    }

    protected function getEmailFormComponent(): Component
    {
        return TextInput::make('email')
            ->label(__('Email or Username'))
            ->required()
            ->autocomplete()
            ->autofocus()
            ->default(config('app.demo_mode') ? 'sarah@demo.jabali-panel.com' : null);
    }

    protected function getPasswordFormComponent(): Component
    {
        $component = parent::getPasswordFormComponent();

        if (config('app.demo_mode')) {
            $component->default('demo');
        }

        return $component;
    }

    /**
     * @param  array<string, mixed>  $data
     * @return array<string, mixed>
     */
    protected function getCredentialsFromFormData(#[SensitiveParameter] array $data): array
    {
        $login = $data['email'];
        $field = str_contains($login, '@') ? 'email' : 'username';

        return [
            $field => $login,
            'password' => $data['password'],
        ];
    }
}
