<?php

declare(strict_types=1);

namespace App\Listeners;

use App\Models\AuditLog;
use Illuminate\Auth\Events\Failed;
use Illuminate\Auth\Events\Login;
use Illuminate\Auth\Events\Logout;
use Illuminate\Auth\Events\PasswordReset;

class AuthEventListener
{
    /**
     * Handle user login events.
     */
    public function handleLogin(Login $event): void
    {
        AuditLog::logAuth('login', $event->user);
    }

    /**
     * Handle user logout events.
     */
    public function handleLogout(Logout $event): void
    {
        if ($event->user) {
            AuditLog::logAuth('logout', $event->user);
        }
    }

    /**
     * Handle failed login attempts.
     */
    public function handleFailed(Failed $event): void
    {
        AuditLog::create([
            'user_id' => $event->user?->id,
            'action' => 'login_failed',
            'category' => 'auth',
            'description' => 'Failed login attempt for: '.($event->credentials['email'] ?? 'unknown'),
            'target_type' => 'user',
            'target_id' => $event->user?->id,
            'target_name' => $event->credentials['email'] ?? 'unknown',
            'ip_address' => request()->ip(),
            'user_agent' => request()->userAgent(),
            'metadata' => [
                'guard' => $event->guard,
            ],
        ]);
    }

    /**
     * Handle password reset events.
     */
    public function handlePasswordReset(PasswordReset $event): void
    {
        AuditLog::logAuth('password_reset', $event->user);
    }

    /**
     * Register the listeners for the subscriber.
     */
    public function subscribe($events): array
    {
        return [
            Login::class => 'handleLogin',
            Logout::class => 'handleLogout',
            Failed::class => 'handleFailed',
            PasswordReset::class => 'handlePasswordReset',
        ];
    }
}
