<?php

declare(strict_types=1);

namespace App\Filament\Admin\Pages\Auth;

use App\Models\User;
use Filament\Auth\Http\Responses\Contracts\LoginResponse;
use Filament\Auth\Pages\Login as BaseLogin;
use Filament\Facades\Filament;
use Illuminate\Support\Facades\Hash;
use Illuminate\Support\HtmlString;

class Login extends BaseLogin
{
    public function getSubheading(): string|HtmlString|null
    {
        if (env('JABALI_DEMO', false)) {
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

        // Check credentials without logging in
        $user = User::where('email', $data['email'])->first();

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
}
