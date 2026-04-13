<?php

declare(strict_types=1);

namespace App\Support;

use Illuminate\Auth\Access\AuthorizationException;
use Illuminate\Support\Facades\Log;
use Illuminate\Validation\ValidationException;

class SafeError
{
    /**
     * Log the full exception and return a safe message for user display.
     */
    public static function message(\Throwable $e, string $fallback = ''): string
    {
        Log::error($e->getMessage(), [
            'exception' => get_class($e),
            'file' => $e->getFile(),
            'line' => $e->getLine(),
        ]);

        if ($e instanceof ValidationException || $e instanceof AuthorizationException) {
            return $e->getMessage();
        }

        if (config('app.debug')) {
            return get_class($e).': '.$e->getMessage().' in '.$e->getFile().':'.$e->getLine();
        }

        return $fallback ?: __('An unexpected error occurred. Please try again.');
    }

    /**
     * Sanitize an error string returned from the agent before displaying it.
     *
     * The agent runs as root and its error strings may contain raw command
     * output — newlines, ANSI codes, absolute paths, anything the underlying
     * tool writes to stderr. This normalizes whitespace, strips control
     * characters, and caps length so the result is safe to drop into a
     * Filament toast notification.
     */
    public static function fromAgent(string $agentError, string $fallback = ''): string
    {
        $fallbackText = $fallback !== '' ? $fallback : __('An unexpected error occurred. Please try again.');

        $agentError = trim($agentError);
        if ($agentError === '') {
            return $fallbackText;
        }

        // Strip control characters (including newlines/tabs) and collapse
        // runs of whitespace into single spaces for single-line display.
        $cleaned = preg_replace('/[\x00-\x1F\x7F]+/u', ' ', $agentError) ?? $agentError;
        $cleaned = preg_replace('/\s+/', ' ', $cleaned) ?? $cleaned;
        $cleaned = trim($cleaned);

        if ($cleaned === '') {
            return $fallbackText;
        }

        // Cap length so one rogue stack trace can't blow out a toast notification.
        if (mb_strlen($cleaned) > 500) {
            $cleaned = mb_substr($cleaned, 0, 497).'...';
        }

        return $cleaned;
    }
}
