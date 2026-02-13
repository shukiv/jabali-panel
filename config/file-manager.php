<?php

return [
    /*
    |--------------------------------------------------------------------------
    | Base Path
    |--------------------------------------------------------------------------
    |
    | This is the base path that will be used for the file manager.
    | It can be a closure that returns the path dynamically.
    |
    */
    'base_path' => null, // We'll set this dynamically per user

    /*
    |--------------------------------------------------------------------------
    | Disk
    |--------------------------------------------------------------------------
    |
    | The filesystem disk to use for file operations.
    |
    */
    'disk' => 'local',

    /*
    |--------------------------------------------------------------------------
    | Max Upload Size
    |--------------------------------------------------------------------------
    |
    | Maximum file upload size in MB.
    |
    */
    'max_upload_size' => 100,

    /*
    |--------------------------------------------------------------------------
    | Allowed Extensions
    |--------------------------------------------------------------------------
    |
    | List of allowed file extensions. Leave empty to allow all.
    |
    */
    'allowed_extensions' => [],

    /*
    |--------------------------------------------------------------------------
    | Forbidden Extensions
    |--------------------------------------------------------------------------
    |
    | List of forbidden file extensions.
    |
    */
    'forbidden_extensions' => [
        'php',
        'phar',
        'sh',
        'bash',
        'exe',
        'bat',
    ],
];
