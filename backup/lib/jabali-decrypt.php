#!/usr/bin/env php
<?php

/**
 * Decrypt a Laravel-encrypted value using the APP_KEY.
 * Usage: php jabali-decrypt.php <encrypted_value>
 * APP_KEY must be set in JABALI_APP_KEY environment variable.
 */
if ($argc < 2) {
    fwrite(STDERR, "Usage: php jabali-decrypt.php <encrypted_value>\n");
    exit(1);
}

$appKey = getenv('JABALI_APP_KEY');
if (! $appKey) {
    fwrite(STDERR, "Error: JABALI_APP_KEY environment variable not set\n");
    exit(1);
}

// Strip base64: prefix if present
if (str_starts_with($appKey, 'base64:')) {
    $appKey = base64_decode(substr($appKey, 7));
}

$encrypted = $argv[1];
$payload = json_decode(base64_decode($encrypted), true);

if (! $payload || ! isset($payload['iv'], $payload['value'], $payload['mac'])) {
    fwrite(STDERR, "Error: Invalid encrypted payload\n");
    exit(1);
}

$iv = base64_decode($payload['iv']);
$value = base64_decode($payload['value']);
$mac = $payload['mac'];

// Verify MAC
$calculated = hash_hmac('sha256', $payload['iv'].$payload['value'], $appKey);
if (! hash_equals($calculated, $mac)) {
    fwrite(STDERR, "Error: MAC verification failed (wrong APP_KEY?)\n");
    exit(1);
}

// Decrypt
$decrypted = openssl_decrypt($value, 'aes-256-cbc', $appKey, OPENSSL_RAW_DATA, $iv);
if ($decrypted === false) {
    fwrite(STDERR, "Error: Decryption failed\n");
    exit(1);
}

// Laravel wraps the value in serialize(), try to unserialize
$result = @unserialize($decrypted);
if ($result === false && $decrypted !== 'b:0;') {
    // Not serialized, output raw
    echo $decrypted;
} else {
    echo $result;
}
