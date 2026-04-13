<?php

declare(strict_types=1);

namespace App\JabaliTerminal;

use App\JabaliTerminal\Http\Controllers\TerminalSessionController;
use Illuminate\Cache\RateLimiting\Limit;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\RateLimiter;
use Illuminate\Support\Facades\Route;
use Illuminate\Support\ServiceProvider;

/**
 * Service provider for the jabali-terminal addon.
 *
 * The rate limiter + the POST /jabali-admin/terminal/session route live
 * here (not in JabaliTerminalPlugin::boot) because Filament's Plugin::boot
 * is called from inside the panel middleware stack — by then Laravel's
 * route collection has already been iterated for URL generation in
 * Blade views (route('jabali-terminal.session')), and it does not run at
 * all during CLI `route:list` / `route:cache`.
 *
 * Registration of this provider is gated in the parent panel's
 * bootstrap/providers.php with `class_exists(...)` so removing the addon
 * cleanly drops the provider with no sed required.
 */
class JabaliTerminalServiceProvider extends ServiceProvider
{
    public function boot(): void
    {
        // Rate limiter keyed by (admin id, ip). Keeps a compromised admin
        // session from minting unlimited tokens, and keeps a single IP from
        // probing across admin ids at full speed.
        RateLimiter::for('jabali-terminal-session', static function (Request $request): Limit {
            $user = $request->user('admin') ?? $request->user();
            $adminId = $user?->getAuthIdentifier() ?? 'guest';

            return Limit::perMinute(3)->by($adminId.'|'.$request->ip());
        });

        // Dedicated auth endpoint. Same /jabali-admin prefix Filament uses
        // so the session cookie + CSRF token scope match.
        if ($this->app->routesAreCached()) {
            return;
        }

        Route::middleware([
            'web',
            'auth:admin',
            'throttle:jabali-terminal-session',
        ])
            ->prefix('jabali-admin')
            ->post('terminal/session', TerminalSessionController::class)
            ->name('jabali-terminal.session');
    }
}
