<?php

declare(strict_types=1);

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Symfony\Component\HttpFoundation\Response;

class MailSubdomainRedirect
{
    /**
     * Redirect mail.* subdomain requests to webmail.
     *
     * Autoconfig/autodiscover paths are excluded so mail clients
     * can still auto-discover settings via the mail hostname.
     */
    public function handle(Request $request, Closure $next): Response
    {
        $host = $request->getHost();

        if (! str_starts_with($host, 'mail.')) {
            return $next($request);
        }

        // Let mail client auto-discovery through
        $path = $request->getPathInfo();
        if (str_starts_with($path, '/autodiscover')
            || str_starts_with($path, '/mail/config')
            || str_starts_with($path, '/.well-known/autoconfig')
            || str_starts_with($path, '/autoconfig')
        ) {
            return $next($request);
        }

        // Already on the webmail path — let it through
        if (str_starts_with($path, '/webmail')) {
            return $next($request);
        }

        $scheme = $request->getScheme();

        return redirect("{$scheme}://{$host}/webmail/");
    }
}
