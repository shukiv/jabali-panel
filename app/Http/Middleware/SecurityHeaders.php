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

        // Content Security Policy — strict as possible while supporting Livewire/Alpine/Filament
        $csp = implode('; ', [
            "default-src 'self'",
            "script-src 'self' 'unsafe-inline' 'unsafe-eval'", // Required for Livewire/Alpine
            "style-src 'self' 'unsafe-inline'", // Required for Tailwind/Filament
            "img-src 'self' data: blob:",
            "font-src 'self' data:",
            "connect-src 'self' https: ws: wss:", // HTTPS for FilePond file preview, WS for Livewire
            "worker-src 'self' blob:", // Required for file upload workers
            "frame-ancestors 'self'",
            "form-action 'self'",
            "base-uri 'self'",
        ]);

        $response->headers->set('Content-Security-Policy', $csp);
        $response->headers->set('Referrer-Policy', 'strict-origin-when-cross-origin');
        $response->headers->set('Permissions-Policy', 'camera=(), microphone=(), geolocation=(), payment=(), usb=(), magnetometer=(), gyroscope=(), accelerometer=()');

        // Cross-origin isolation headers (CORP, COOP, COEP also set by nginx for static assets)
        if (! $response->headers->has('Cross-Origin-Resource-Policy')) {
            $response->headers->set('Cross-Origin-Resource-Policy', 'same-origin');
        }
        if (! $response->headers->has('Cross-Origin-Opener-Policy')) {
            $response->headers->set('Cross-Origin-Opener-Policy', 'same-origin');
        }
        if (! $response->headers->has('Cross-Origin-Embedder-Policy')) {
            $response->headers->set('Cross-Origin-Embedder-Policy', 'credentialless');
        }

        // X-Content-Type-Options and X-Frame-Options — nginx also sets these + HSTS
        $response->headers->set('X-Content-Type-Options', 'nosniff');
        $response->headers->set('X-Frame-Options', 'SAMEORIGIN');

        // Strip body from redirect responses to prevent information leakage
        if ($response->isRedirection()) {
            $response->setContent('');
        }

        return $response;
    }
}
