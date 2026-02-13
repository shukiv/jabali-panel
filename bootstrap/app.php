<?php

use Illuminate\Foundation\Application;
use Illuminate\Foundation\Configuration\Exceptions;
use Illuminate\Foundation\Configuration\Middleware;
use Illuminate\Http\Request;

return Application::configure(basePath: dirname(__DIR__))
    ->withRouting(
        web: __DIR__.'/../routes/web.php',
        api: __DIR__.'/../routes/api.php',
        commands: __DIR__.'/../routes/console.php',
        health: '/up',
    )
    ->withMiddleware(function (Middleware $middleware): void {
        $trustedProxies = env('TRUSTED_PROXIES');
        $resolvedProxies = match (true) {
            is_string($trustedProxies) && trim($trustedProxies) === '*' => '*',
            is_string($trustedProxies) && trim($trustedProxies) !== '' => array_values(array_filter(array_map(
                static fn (string $proxy): string => trim($proxy),
                explode(',', $trustedProxies)
            ))),
            default => ['127.0.0.1', '::1'],
        };

        $middleware->trustProxies(
            at: $resolvedProxies,
            headers: Request::HEADER_X_FORWARDED_FOR
                | Request::HEADER_X_FORWARDED_HOST
                | Request::HEADER_X_FORWARDED_PORT
                | Request::HEADER_X_FORWARDED_PROTO
        );
        $middleware->throttleApi('api');
        $middleware->append(\App\Http\Middleware\SecurityHeaders::class);
    })
    ->withExceptions(function (Exceptions $exceptions): void {
        //
    })->create();
