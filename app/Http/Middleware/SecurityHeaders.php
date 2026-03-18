<?php

declare(strict_types=1);

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

class SecurityHeaders
{
    /**
     * Security headers to add to all responses.
     */
    public function handle(Request $request, Closure $next): Response
    {
        $response = $next($request);

        // Content Security Policy
        $csp = implode('; ', [
            "default-src 'self'",
            "script-src 'self' 'unsafe-inline' 'unsafe-eval'", // Required for Livewire/Alpine
            "style-src 'self' 'unsafe-inline'", // Required for Tailwind/Filament
            "img-src 'self' data: blob:",
            "font-src 'self' data:",
            "connect-src 'self' ws: wss:", // WebSocket for Livewire
            "worker-src 'self' blob:", // Required for file upload workers
            "frame-ancestors 'self'",
            "form-action 'self'",
            "base-uri 'self'",
        ]);

        $response->headers->set('Content-Security-Policy', $csp);
        $response->headers->set('Referrer-Policy', 'strict-origin-when-cross-origin');
        $response->headers->set('Permissions-Policy', 'camera=(), microphone=(), geolocation=()');
        $response->headers->set('Cross-Origin-Resource-Policy', 'same-origin');

        // X-Content-Type-Options and X-Frame-Options are set here as fallback.
        // Nginx also sets these + HSTS via add_header — we skip HSTS here
        // to avoid duplicate header warnings from security scanners.
        $response->headers->set('X-Content-Type-Options', 'nosniff');
        $response->headers->set('X-Frame-Options', 'SAMEORIGIN');

        return $response;
    }
}
