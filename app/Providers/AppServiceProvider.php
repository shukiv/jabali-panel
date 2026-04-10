<?php

declare(strict_types=1);

namespace App\Providers;

use App\Models\Domain;
use App\Observers\DomainObserver;
use App\Services\Agent\AgentClient;
use App\Services\Agent\AgentClientInterface;
use App\Services\Agent\DemoAgentClient;
use Filament\Actions\Action;
use Filament\Support\Facades\FilamentAsset;
use Illuminate\Cache\RateLimiting\Limit;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\File;
use Illuminate\Support\Facades\Gate;
use Illuminate\Support\Facades\RateLimiter;
use Illuminate\Support\ServiceProvider;

class AppServiceProvider extends ServiceProvider
{
    /**
     * Register any application services.
     */
    public function register(): void
    {
        $this->app->singleton(AgentClient::class, fn (): AgentClient => config('app.demo_mode')
            ? new DemoAgentClient
            : new AgentClient(
                (string) config('jabali.agent.socket', '/var/run/jabali/agent.sock'),
                (int) config('jabali.agent.timeout', 30),
            )
        );
        $this->app->alias(AgentClient::class, AgentClientInterface::class);
    }

    /**
     * Bootstrap any application services.
     */
    public function boot(): void
    {
        Domain::observe(DomainObserver::class);

        // Demo mode: block all write operations
        if (config('app.demo_mode')) {
            // Hide create/edit/delete buttons on Filament resources
            Gate::before(function ($user, $ability) {
                if (in_array($ability, ['create', 'update', 'delete', 'forceDelete', 'restore', 'deleteAny', 'forceDeleteAny', 'restoreAny'])) {
                    return false;
                }

                return null;
            });

            // Hide all Filament actions that modify data (covers custom pages too)
            Action::configureUsing(function (Action $action): void {
                $name = strtolower($action->getName());
                $writePatterns = ['create', 'save', 'delete', 'edit', 'new', 'install', 'remove', 'update', 'disable', 'enable', 'suspend', 'reboot', 'restart'];

                foreach ($writePatterns as $pattern) {
                    if (str_contains($name, $pattern)) {
                        $action->hidden();

                        return;
                    }
                }
            });

            // Block database writes by using a read-only DB connection
            // Override the default connection to use a read-only MySQL user
            // This is set up by the demo provisioning script
        }

        // Override jabali-file-browser's adapter with the agent-backed adapter
        // Must be in boot() to run after the package's register()
        $this->app->bind(\App\FileBrowser\Adapters\FileBrowserAdapter::class, function ($app) {
            $user = \Illuminate\Support\Facades\Auth::user();
            $username = $user?->username ?? 'nobody';

            return new \App\Services\FileBrowser\AgentAdapter(
                $app->make(AgentClient::class),
                $username,
            );
        });

        // Override trash manager with agent-backed version
        $this->app->singleton(\App\FileBrowser\Services\TrashManager::class, function ($app) {
            $user = \Illuminate\Support\Facades\Auth::user();
            $username = $user?->username ?? 'nobody';

            return new \App\Services\FileBrowser\AgentTrashManager(
                $app->make(AgentClient::class),
                $username,
            );
        });

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

        // Note: AuthEventListener is auto-discovered by Laravel 11+
        // Do not manually subscribe - it causes duplicate audit log entries
    }
}
