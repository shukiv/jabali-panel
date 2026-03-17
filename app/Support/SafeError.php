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

        return $fallback ?: __('An unexpected error occurred. Please try again.');
    }
}
