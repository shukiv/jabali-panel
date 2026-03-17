<?php

namespace App\Providers;

use App\Models\Domain;
use App\Observers\DomainObserver;
use Filament\Support\Assets\Js;
use Filament\Support\Facades\FilamentAsset;
use Illuminate\Cache\RateLimiting\Limit;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\File;
use Illuminate\Support\Facades\RateLimiter;
use Illuminate\Support\Facades\Vite;
use Illuminate\Support\ServiceProvider;

class AppServiceProvider extends ServiceProvider
{
    /**
     * Register any application services.
     */
    public function register(): void {}

    /**
     * Bootstrap any application services.
     */
    public function boot(): void
    {
        Domain::observe(DomainObserver::class);

        RateLimiter::for('api', function (Request $request): array {
            $identifier = $request->user()?->getAuthIdentifier() ?? $request->ip();

            return [
                Limit::perMinute(120)->by('api:'.$identifier),
            ];
        });

        RateLimiter::for('internal-api', function (Request $request): array {
            $remoteAddr = (string) $request->server('REMOTE_ADDR', $request->ip());

            return [
                Limit::perMinute(60)->by('internal:'.$remoteAddr),
            ];
        });

        RateLimiter::for('git-webhooks', function (Request $request): array {
            $deploymentId = $request->route('deployment');
            $deploymentKey = is_object($deploymentId) ? (string) $deploymentId->getKey() : (string) $deploymentId;

            return [
                Limit::perMinute(120)->by('webhook:'.$deploymentKey.':'.$request->ip()),
            ];
        });

        $versionFile = base_path('VERSION');
        $appVersion = File::exists($versionFile) ? trim(File::get($versionFile)) : null;
        FilamentAsset::appVersion($appVersion ?: null);

        FilamentAsset::register([
            Js::make('chart-plugins', Vite::asset('resources/js/chart-plugins.js'))->module(),
        ]);

        // Note: AuthEventListener is auto-discovered by Laravel 11+
        // Do not manually subscribe - it causes duplicate audit log entries
    }
}
