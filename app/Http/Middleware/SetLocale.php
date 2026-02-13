<?php

namespace App\Http\Middleware;

use Closure;
use Illuminate\Http\Request;
use Illuminate\Support\Facades\App;
use Illuminate\Support\Facades\Session;
use Symfony\Component\HttpFoundation\Response;

class SetLocale
{
    /**
     * Handle an incoming request.
     *
     * @param  \Closure(\Illuminate\Http\Request): (\Symfony\Component\HttpFoundation\Response)  $next
     */
    public function handle(Request $request, Closure $next): Response
    {
        // Get supported languages
        $supportedLanguages = array_keys(config('languages.supported', ['en' => []]));
        $defaultLanguage = config('languages.default', 'en');
        $sessionKey = config('languages.session_key', 'locale');

        // Priority: 1. Session, 2. User preference, 3. Browser, 4. Default
        $locale = null;

        // Check session
        if (Session::has($sessionKey)) {
            $locale = Session::get($sessionKey);
        }

        // Check authenticated user's preference
        if (!$locale && auth()->check() && auth()->user()->locale) {
            $locale = auth()->user()->locale;
            Session::put($sessionKey, $locale);
        }

        // Check browser's Accept-Language header
        if (!$locale) {
            $browserLocale = $this->getBrowserLocale($request, $supportedLanguages);
            if ($browserLocale) {
                $locale = $browserLocale;
            }
        }

        // Fallback to default
        if (!$locale || !in_array($locale, $supportedLanguages)) {
            $locale = $defaultLanguage;
        }

        // Set the application locale
        App::setLocale($locale);

        // Set text direction for RTL languages
        $direction = config("languages.supported.{$locale}.direction", 'ltr');
        view()->share('textDirection', $direction);
        view()->share('currentLocale', $locale);

        return $next($request);
    }

    /**
     * Get the browser's preferred locale from Accept-Language header.
     */
    protected function getBrowserLocale(Request $request, array $supportedLanguages): ?string
    {
        $acceptLanguage = $request->header('Accept-Language');

        if (!$acceptLanguage) {
            return null;
        }

        // Parse Accept-Language header
        $languages = [];
        foreach (explode(',', $acceptLanguage) as $lang) {
            $parts = explode(';q=', trim($lang));
            $code = strtolower(substr($parts[0], 0, 2));
            $quality = isset($parts[1]) ? (float) $parts[1] : 1.0;
            $languages[$code] = $quality;
        }

        // Sort by quality
        arsort($languages);

        // Find first supported language
        foreach (array_keys($languages) as $code) {
            if (in_array($code, $supportedLanguages)) {
                return $code;
            }
        }

        return null;
    }
}
