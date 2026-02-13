<?php

declare(strict_types=1);

namespace App\Services;

use Exception;
use Illuminate\Support\Facades\Log;

/**
 * Manages the Jabali system SSH key used for connecting to remote servers
 */
class JabaliSshKey
{
    private const KEY_DIR = '/etc/jabali/ssh';

    private const KEY_NAME = 'jabali_rsa';

    /**
     * Get the path to the private key
     */
    public static function getPrivateKeyPath(): string
    {
        return self::KEY_DIR.'/'.self::KEY_NAME;
    }

    /**
     * Get the path to the public key
     */
    public static function getPublicKeyPath(): string
    {
        return self::KEY_DIR.'/'.self::KEY_NAME.'.pub';
    }

    /**
     * Check if the SSH key exists
     */
    public static function exists(): bool
    {
        return file_exists(self::getPrivateKeyPath()) && file_exists(self::getPublicKeyPath());
    }

    /**
     * Get the public key content
     */
    public static function getPublicKey(): ?string
    {
        $path = self::getPublicKeyPath();

        if (! file_exists($path)) {
            return null;
        }

        return trim(file_get_contents($path));
    }

    /**
     * Get the private key content
     */
    public static function getPrivateKey(): ?string
    {
        $path = self::getPrivateKeyPath();

        if (! file_exists($path)) {
            return null;
        }

        return file_get_contents($path);
    }

    /**
     * Get the key name used when importing to cPanel
     */
    public static function getKeyName(): string
    {
        return 'jabali-system-key';
    }

    /**
     * Generate a new SSH key pair if one doesn't exist
     *
     * @return bool True if key was generated or already exists
     */
    public static function ensureExists(): bool
    {
        if (self::exists()) {
            return true;
        }

        return self::generate();
    }

    /**
     * Generate a new SSH key pair
     */
    public static function generate(): bool
    {
        try {
            // Create directory if it doesn't exist
            if (! is_dir(self::KEY_DIR)) {
                mkdir(self::KEY_DIR, 0700, true);
            }

            $keyPath = self::getPrivateKeyPath();

            // Generate the key
            $command = sprintf(
                'ssh-keygen -t rsa -b 4096 -f %s -N "" -C "jabali-system-key" 2>&1',
                escapeshellarg($keyPath)
            );

            exec($command, $output, $returnCode);

            if ($returnCode !== 0) {
                Log::error('Failed to generate SSH key', ['output' => implode("\n", $output)]);

                return false;
            }

            // Set proper permissions
            chmod($keyPath, 0600);
            chmod($keyPath.'.pub', 0644);

            Log::info('Jabali system SSH key generated');

            return true;
        } catch (Exception $e) {
            Log::error('Failed to generate SSH key: '.$e->getMessage());

            return false;
        }
    }

    /**
     * Get the fingerprint of the public key
     */
    public static function getFingerprint(): ?string
    {
        $pubKeyPath = self::getPublicKeyPath();

        if (! file_exists($pubKeyPath)) {
            return null;
        }

        exec('ssh-keygen -lf '.escapeshellarg($pubKeyPath).' 2>&1', $output, $returnCode);

        if ($returnCode !== 0) {
            return null;
        }

        return $output[0] ?? null;
    }
}
