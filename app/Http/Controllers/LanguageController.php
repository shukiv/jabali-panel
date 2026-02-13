<?php

namespace App\Http\Controllers;

use Illuminate\Http\Request;
use Illuminate\Support\Facades\Session;

class LanguageController extends Controller
{
    /**
     * Switch the application language.
     */
    public function switch(Request $request, string $locale)
    {
        $supportedLanguages = array_keys(config('languages.supported', ['en' => []]));

        if (!in_array($locale, $supportedLanguages)) {
            abort(400, 'Unsupported language');
        }

        // Store in session
        Session::put(config('languages.session_key', 'locale'), $locale);

        // Update user preference if authenticated
        if (auth()->check()) {
            auth()->user()->update(['locale' => $locale]);
        }

        return redirect()->back();
    }
}
