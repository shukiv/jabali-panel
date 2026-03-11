<?php

return [
    /*
    |--------------------------------------------------------------------------
    | Supported Languages
    |--------------------------------------------------------------------------
    |
    | Define the languages supported by the application. Each language should
    | have a code, name, native name, and direction (ltr/rtl).
    |
    */

    'supported' => [
        'en' => [
            'name' => 'English',
            'native' => 'English',
            'direction' => 'ltr',
        ],
        'es' => [
            'name' => 'Spanish',
            'native' => 'Español',
            'direction' => 'ltr',
        ],
        'ar' => [
            'name' => 'Arabic',
            'native' => 'العربية',
            'direction' => 'rtl',
        ],
        'fr' => [
            'name' => 'French',
            'native' => 'Français',
            'direction' => 'ltr',
        ],
        'ru' => [
            'name' => 'Russian',
            'native' => 'Русский',
            'direction' => 'ltr',
        ],
        'pt' => [
            'name' => 'Portuguese',
            'native' => 'Português',
            'direction' => 'ltr',
        ],
        'he' => [
            'name' => 'Hebrew',
            'native' => 'עברית',
            'direction' => 'rtl',
        ],
    ],

    /*
    |--------------------------------------------------------------------------
    | Default Language
    |--------------------------------------------------------------------------
    |
    | The default language to use when no preference is set.
    |
    */

    'default' => 'en',

    /*
    |--------------------------------------------------------------------------
    | Session Key
    |--------------------------------------------------------------------------
    |
    | The session key used to store the user's language preference.
    |
    */

    'session_key' => 'locale',
];
