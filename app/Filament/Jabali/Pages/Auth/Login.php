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
use Illuminate\Support\HtmlString;
use SensitiveParameter;

class Login extends BaseLogin
{
    public function getSubheading(): string|HtmlString|null
    {
        if (config('jabali.demo')) {
            return new HtmlString(
                __('Demo credentials').
                ': <code>demo@jabali-panel.com</code> / <code>demo1234</code>'
            );
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

        /** @var Guard $authGuard */
        $authGuard = Auth::guard($panel->getAuthGuard());
        $authProvider = $authGuard->getProvider();
        $credentials = $this->getCredentialsFromFormData($data);

        $user = $authProvider->retrieveByCredentials($credentials);

        if ((! $user) || (! $authProvider->validateCredentials($user, $credentials))) {
            $this->userUndertakingMultiFactorAuthentication = null;

            $this->fireFailedEvent($authGuard, $user, $credentials);
            $this->throwFailureValidationException();
        }

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

        // If authentication successful, check if user is admin
        $user = $authGuard->user();
        if ($user && $user->is_admin) {
            $authGuard->logout();

            // Redirect admins to admin panel using Livewire's redirect
            $this->redirect(route('filament.admin.pages.dashboard'));

            return null;
        }

        return app(LoginResponse::class);
    }

    protected function getEmailFormComponent(): Component
    {
        return TextInput::make('email')
            ->label(__('Email or Username'))
            ->required()
            ->autocomplete()
            ->autofocus();
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
