<?php

declare(strict_types=1);

namespace App\Http\Controllers;

use App\Models\ImpersonationToken;
use App\Models\User;
use Illuminate\Http\RedirectResponse;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\Auth;

class ImpersonationController extends Controller
{
    /**
     * Start impersonation - generates token and redirects to impersonate
     * Used by admin panel to initiate impersonation via a clickable link
     */
    public function start(Request $request, User $user): RedirectResponse
    {
        // Use the admin guard to check authentication
        $admin = Auth::guard('admin')->user();

        // Verify admin has permission to impersonate
        if (! $admin || ! $admin->is_admin) {
            abort(403, 'Unauthorized');
        }

        // Cannot impersonate other admins
        if ($user->is_admin) {
            abort(403, 'Cannot impersonate administrators');
        }

        // Cannot impersonate inactive users
        if (! $user->is_active) {
            abort(403, 'Cannot impersonate inactive users');
        }

        // Create the impersonation token
        $token = ImpersonationToken::createForUser($admin, $user, $request->ip());

        // Redirect to the impersonation route
        return redirect()->route('impersonate', ['token' => $token->token]);
    }

    public function impersonate(Request $request, string $token): RedirectResponse
    {
        $impersonationToken = ImpersonationToken::findValidToken($token, $request->ip());

        if (! $impersonationToken) {
            return redirect()->route('filament.admin.pages.dashboard')
                ->with('error', 'Invalid or expired impersonation token.');
        }

        // Mark token as used
        $impersonationToken->markAsUsed();

        // Get the target user
        $targetUser = $impersonationToken->targetUser;

        if (! $targetUser || ! $targetUser->is_active) {
            return redirect()->route('filament.admin.pages.dashboard')
                ->with('error', 'User not found or inactive.');
        }

        // Store the admin ID before any session changes
        $adminId = $impersonationToken->admin_id;

        // Clear any previous impersonation session data first
        // This prevents session corruption when impersonating multiple users
        session()->forget('impersonated_by');
        session()->forget('impersonation_token_id');

        // Directly set the user on the web guard without full logout
        // This avoids session invalidation that would affect the admin guard
        Auth::guard('web')->setUser($targetUser);

        // Update the session with the new user's ID for the web guard
        session()->put(Auth::guard('web')->getName(), $targetUser->getAuthIdentifier());

        // Update the password hash in session for AuthenticateSession middleware
        // This prevents the middleware from logging out the user due to hash mismatch
        session()->put('password_hash_web', $targetUser->getAuthPassword());

        // Store impersonation info in session
        session()->put('impersonated_by', $adminId);
        session()->put('impersonation_token_id', $impersonationToken->id);

        // Save the session to persist changes
        session()->save();

        // Redirect to user panel
        return redirect()->route('filament.jabali.pages.dashboard');
    }

    /**
     * Stop impersonation and return to admin panel
     */
    public function stop(): RedirectResponse
    {
        // Verify we are actually impersonating (M1 fix)
        if (! session()->has('impersonated_by')) {
            return redirect()->route('filament.jabali.pages.dashboard');
        }

        // Clear impersonation session data
        session()->forget('impersonated_by');
        session()->forget('impersonation_token_id');

        // Clear the web guard session data without full logout
        // This preserves the admin session
        session()->forget(Auth::guard('web')->getName());
        session()->forget('password_hash_web');

        // Save session changes
        session()->save();

        // Redirect back to admin panel
        return redirect()->route('filament.admin.pages.dashboard')
            ->with('success', 'Returned to admin account.');
    }
}
