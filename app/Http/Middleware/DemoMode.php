<?php

declare(strict_types=1);

namespace App\Http\Middleware;

use Closure;
use Filament\Notifications\Notification;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * Demo mode middleware — blocks destructive operations.
 *
 * Only active when DEMO_MODE=true in .env. Gate::before() in AppServiceProvider
 * handles Filament resource authorization (hides create/edit/delete buttons).
 * This middleware catches remaining write attempts.
 */
class DemoMode
{
    /** Livewire methods that modify data — block these in demo mode */
    private const BLOCKED_METHODS = [
        'create',
        'save',
        'delete',
        'mountAction',
        'callMountedAction',
        'callMountedTableAction',
    ];

    public function handle(Request $request, Closure $next): Response
    {
        if (! config('app.demo_mode')) {
            return $next($request);
        }

        // Allow all GET requests
        if ($request->isMethod('GET')) {
            return $next($request);
        }

        // Allow login POST
        if ($request->is('*/login')) {
            return $next($request);
        }

        // Block logout and password changes
        if ($request->is('*/logout', '*/password*', '*/two-factor*')) {
            abort(403, 'Demo mode — this action is disabled.');
        }

        // For Livewire requests, check if it's a write operation
        if ($this->isLivewireRequest($request) && $this->isWriteOperation($request)) {
            // Send a Filament notification and return the Livewire response
            Notification::make()
                ->title(__('Demo Mode'))
                ->body(__('Create, edit, and delete operations are disabled in the demo.'))
                ->warning()
                ->send();

            // Let it through — the Gate::before() will deny the actual operation
            // and Filament will show an authorization error
        }

        return $next($request);
    }

    private function isLivewireRequest(Request $request): bool
    {
        return $request->hasHeader('X-Livewire');
    }

    private function isWriteOperation(Request $request): bool
    {
        $payload = $request->input('components.0.calls', []);

        foreach ($payload as $call) {
            $method = $call['method'] ?? '';
            foreach (self::BLOCKED_METHODS as $blocked) {
                if (str_contains($method, $blocked)) {
                    return true;
                }
            }
        }

        return false;
    }
}
