<?php

declare(strict_types=1);

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

class RedirectAdminFromUserPanel
{
    /**
     * Handle an incoming request.
     *
     * Redirects admin users away from the user panel to the admin panel.
     * Allows impersonation (when an admin is viewing as a user).
     */
    public function handle(Request $request, Closure $next): Response
    {
        // Get user from 'web' guard (user panel guard)
        $user = $request->user('web');

        // If the user on the web guard is an admin AND not being impersonated,
        // redirect to admin panel
        if ($user && $user->is_admin && !session()->has('impersonated_by')) {
            return redirect()->route('filament.admin.pages.dashboard');
        }

        return $next($request);
    }
}
