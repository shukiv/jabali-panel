<?php

return [
    /*
    |--------------------------------------------------------------------------
    | Supported Languages
    |--------------------------------------------------------------------------
    |
    | Define the languages supported by the application. Each language should
    | have a code, name, native name, direction (ltr/rtl), and flag emoji.
    |
    */

    'supported' => [
        'en' => [
            'name' => 'English',
            'native' => 'English',
            'direction' => 'ltr',
            'flag' => '🇬🇧',
        ],
        'es' => [
            'name' => 'Spanish',
            'native' => 'Español',
            'direction' => 'ltr',
            'flag' => '🇪🇸',
        ],
        'ar' => [
            'name' => 'Arabic',
            'native' => 'العربية',
            'direction' => 'rtl',
            'flag' => '🇸🇦',
        ],
        'fr' => [
            'name' => 'French',
            'native' => 'Français',
            'direction' => 'ltr',
            'flag' => '🇫🇷',
        ],
        'ru' => [
            'name' => 'Russian',
            'native' => 'Русский',
            'direction' => 'ltr',
            'flag' => '🇷🇺',
        ],
        'pt' => [
            'name' => 'Portuguese',
            'native' => 'Português',
            'direction' => 'ltr',
            'flag' => '🇧🇷',
        ],
        'he' => [
            'name' => 'Hebrew',
            'native' => 'עברית',
            'direction' => 'rtl',
            'flag' => '🇮🇱',
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
