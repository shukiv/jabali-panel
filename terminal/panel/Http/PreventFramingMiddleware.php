<?php

declare(strict_types=1);

namespace App\JabaliTerminal\Http;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

/**
 * Enforces SEC-REV-8: the terminal page MUST NOT be frameable.
 *
 * Both X-Frame-Options: DENY and CSP frame-ancestors 'none' are set because
 * legacy browsers and embedded webviews still consult X-Frame-Options.
 */
class PreventFramingMiddleware
{
    public function handle(Request $request, Closure $next): Response
    {
        $response = $next($request);

        $response->headers->set('X-Frame-Options', 'DENY');

        // Merge with any existing CSP so a panel-wide policy is not clobbered.
        $existing = $response->headers->get('Content-Security-Policy');
        $directive = "frame-ancestors 'none'";
        if ($existing) {
            if (! str_contains($existing, 'frame-ancestors')) {
                $response->headers->set('Content-Security-Policy', rtrim($existing, '; ').'; '.$directive);
            }
        } else {
            $response->headers->set('Content-Security-Policy', $directive);
        }

        return $response;
    }
}
