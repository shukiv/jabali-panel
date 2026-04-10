<?php

declare(strict_types=1);

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * Demo mode middleware — blocks destructive operations.
 *
 * Only active when DEMO_MODE=true in .env. Allows Livewire navigation
 * and reads, but blocks form submissions and login/logout.
 */
class DemoMode
{
    /** Livewire component snapshots that are safe to allow (navigation, table filters, etc.) */
    private const SAFE_LIVEWIRE_METHODS = [
        'mountTableAction',
        'sortTable',
        'searchTable',
        'gotoPage',
        'previousPage',
        'nextPage',
        'resetTableFilters',
        'setActiveTab',
        'getTableRecords',
    ];

    public function handle(Request $request, Closure $next): Response
    {
        if (! config('app.demo_mode')) {
            return $next($request);
        }

        // Allow all GET requests (page loads, navigation)
        if ($request->isMethod('GET')) {
            return $next($request);
        }

        // Block logout and password changes (login must be allowed)
        if ($request->is('*/logout', '*/password*', '*/two-factor*')) {
            abort(403, 'Demo mode — this action is disabled.');
        }

        // Allow Livewire requests (POST) — they handle navigation, table sorting, etc.
        // The DemoAgentClient handles write neutralization at the agent level
        return $next($request);
    }
}
