<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages\Auth;

use App\Models\User;
use Filament\Auth\Http\Responses\Contracts\LoginResponse;
use Filament\Auth\Pages\Login as BaseLogin;
use Filament\Facades\Filament;
use Filament\Forms\Components\TextInput;
use Filament\Schemas\Components\Component;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\HtmlString;
use SensitiveParameter;

class Login extends BaseLogin
{
    public function getSubheading(): string|HtmlString|null
    {
        if (config('jabali.demo')) {
            return new HtmlString(
                __('Demo credentials').
                ': <code>admin@jabali-panel.com</code> / <code>demo1234</code>'
            );
        }

        return parent::getSubheading();
    }

    public function authenticate(): ?LoginResponse
    {
        $data = $this->form->getState();

        // Check credentials without logging in — support email or username
        $login = $data['email'];
        $user = str_contains($login, '@')
            ? User::where('email', $login)->first()
            : User::where('username', $login)->first();

        if (! $user) {
            // Constant-time: prevent user enumeration via timing
            Hash::check($data['password'], '$2y$12$VG9EWpdymMd.phHpJLWwauaoYk0mufInpXAzNUqZcMXm/WszkDs42');
        }

        if ($user && Hash::check($data['password'], $user->password)) {
            if (! $user->is_admin) {
                $this->redirect(route('filament.jabali.pages.dashboard'));

                return null;
            }

            // Check if 2FA is enabled
            if ($user->two_factor_secret && $user->two_factor_confirmed_at) {
                // Store user ID in session for 2FA challenge
                session(['login.id' => $user->id]);
                session(['login.remember' => $data['remember'] ?? false]);

                // Redirect to 2FA challenge
                $this->redirect(route('filament.admin.auth.two-factor-challenge'));

                return null;
            }
        }

        $response = parent::authenticate();

        // If authentication successful, check if user is NOT admin
        $user = Filament::auth()->user();
        if ($user && ! $user->is_admin) {
            // Log out from admin guard - regular users can't access admin panel
            Filament::auth()->logout();

            // Redirect to user panel using Livewire's redirect
            $this->redirect(route('filament.jabali.pages.dashboard'));

            return null;
        }

        return $response;
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
