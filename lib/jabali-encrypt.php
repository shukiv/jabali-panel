#!/usr/bin/env php
<?php

declare(strict_types=1);

/**
 * Encrypt a plaintext value using Laravel's encryption format.
 * Usage: php jabali-encrypt.php <plaintext_value>
 * APP_KEY must be set in JABALI_APP_KEY environment variable.
 */

if ($argc < 2) {
    fwrite(STDERR, "Usage: php jabali-encrypt.php <plaintext_value>\n");
    exit(1);
}

$appKey = getenv('JABALI_APP_KEY');
if (!$appKey) {
    fwrite(STDERR, "Error: JABALI_APP_KEY environment variable not set\n");
    exit(1);
}

if (str_starts_with($appKey, 'base64:')) {
    $appKey = base64_decode(substr($appKey, 7));
}

$plaintext = $argv[1];

// Laravel serializes the value before encrypting
$serialized = serialize($plaintext);

$iv = random_bytes(16);
$encrypted = openssl_encrypt($serialized, 'aes-256-cbc', $appKey, OPENSSL_RAW_DATA, $iv);

if ($encrypted === false) {
    fwrite(STDERR, "Error: Encryption failed\n");
    exit(1);
}

$ivB64 = base64_encode($iv);
$valueB64 = base64_encode($encrypted);
$mac = hash_hmac('sha256', $ivB64 . $valueB64, $appKey);

$payload = json_encode([
    'iv' => $ivB64,
    'value' => $valueB64,
    'mac' => $mac,
    'tag' => '',
], JSON_THROW_ON_ERROR);

echo base64_encode($payload);
